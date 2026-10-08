package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// validationError is a bad-input error found while writing (it needed the
// database to decide), reported to the client as 400 rather than 500.
type validationError struct{ msg string }

func (e validationError) Error() string { return e.msg }

// ---- Excludes -------------------------------------------------------------------------

var globChars = "*?[{"

func validateExcludes(ex []string) ([]string, error) {
	if len(ex) > 100 {
		return nil, errors.New("too many excludes (max 100)")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, e := range ex {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		if len(e) > 512 || strings.ContainsAny(e, "\x00\r\n") {
			return nil, fmt.Errorf("invalid exclude %q", e)
		}
		if strings.HasPrefix(e, "/") && !strings.ContainsAny(e, globChars) && path.Clean(e) != e {
			return nil, fmt.Errorf("exclude %q must be a clean absolute path", e)
		}
		seen[e] = true
		out = append(out, e)
	}
	return out, nil
}

// ---- Database dumps -------------------------------------------------------------------

type dumpView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Container       string   `json:"container"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	User            string   `json:"user"`
	HasPassword     bool     `json:"has_password"`
	UseContainerEnv bool     `json:"use_container_env"`
	Databases       []string `json:"databases"`
	KeepDays        int      `json:"keep_days"`
}

type dumpInput struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Container       string   `json:"container"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	User            string   `json:"user"`
	Password        string   `json:"password"` // blank on edit = keep the stored one
	UseContainerEnv bool     `json:"use_container_env"`
	Databases       []string `json:"databases"`
	KeepDays        *int     `json:"keep_days"`
}

var (
	dumpNameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	dbHostRe    = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]{1,253}$`)
	dbUserRe    = regexp.MustCompile(`^[A-Za-z0-9_.@%-]{1,80}$`)
	dbNameRe    = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)
)

func (d *dumpInput) validate() error {
	d.Name, d.Container, d.Host, d.User = strings.TrimSpace(d.Name), strings.TrimSpace(d.Container), strings.TrimSpace(d.Host), strings.TrimSpace(d.User)
	if !dumpNameRe.MatchString(d.Name) {
		return fmt.Errorf("database dump name %q: use letters, digits, . _ - (it becomes part of the file name)", d.Name)
	}
	switch {
	case d.Container != "" && d.Host != "":
		return fmt.Errorf("%s: choose a Docker container or a host, not both", d.Name)
	case d.Container != "":
		if !containerRe.MatchString(d.Container) {
			return fmt.Errorf("%s: invalid container name", d.Name)
		}
		d.Port = 0
	case d.Host != "":
		if !dbHostRe.MatchString(d.Host) {
			return fmt.Errorf("%s: invalid host", d.Name)
		}
		if d.Port == 0 {
			d.Port = 3306
		}
		if d.Port < 1 || d.Port > 65535 {
			return fmt.Errorf("%s: invalid port", d.Name)
		}
		if d.UseContainerEnv {
			return fmt.Errorf("%s: the container's password can only be used with a Docker container", d.Name)
		}
	default:
		return fmt.Errorf("%s: enter a Docker container name or a database host", d.Name)
	}
	if d.User == "" {
		d.User = "root"
	}
	if !dbUserRe.MatchString(d.User) {
		return fmt.Errorf("%s: invalid user name", d.Name)
	}
	if d.UseContainerEnv {
		d.Password = ""
	}
	if len(d.Password) > 256 || strings.ContainsAny(d.Password, "\x00\r\n") {
		return fmt.Errorf("%s: invalid password", d.Name)
	}
	dbs := []string{}
	for _, db := range d.Databases {
		if db = strings.TrimSpace(db); db == "" {
			continue
		}
		if !dbNameRe.MatchString(db) {
			return fmt.Errorf("%s: invalid database name %q", d.Name, db)
		}
		dbs = append(dbs, db)
	}
	if len(dbs) > 50 {
		return fmt.Errorf("%s: too many databases (max 50); leave empty to dump all", d.Name)
	}
	d.Databases = dbs
	if d.KeepDays == nil {
		k := 30
		d.KeepDays = &k
	}
	if *d.KeepDays < 0 || *d.KeepDays > 3650 {
		return fmt.Errorf("%s: keep dumps for 0 (forever) to 3650 days", d.Name)
	}
	return nil
}

func validateDumps(ds []dumpInput) error {
	if len(ds) > 10 {
		return errors.New("too many database dumps (max 10)")
	}
	names := map[string]bool{}
	for i := range ds {
		if err := ds[i].validate(); err != nil {
			return err
		}
		if names[ds[i].Name] {
			return fmt.Errorf("two database dumps are named %q", ds[i].Name)
		}
		names[ds[i].Name] = true
	}
	return nil
}

func dumpAAD(id string) string { return "database_dumps.password:" + id }

// writeJobExtras stores excludes and dumps for a job inside the caller's
// transaction. A dump whose password is left blank keeps its stored one.
func (s *Server) writeJobExtras(tx *sql.Tx, jobID string, in *jobInput) error {
	ex, _ := json.Marshal(in.Excludes)
	if _, err := tx.Exec(`UPDATE backup_jobs SET excludes = ? WHERE id = ?`, string(ex), jobID); err != nil {
		return err
	}
	old := map[string]string{} // dump id -> password_enc
	rows, err := tx.Query(`SELECT id, password_enc FROM database_dumps WHERE job_id = ?`, jobID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, enc string
		if rows.Scan(&id, &enc) == nil {
			old[id] = enc
		}
	}
	rows.Close()
	if _, err := tx.Exec(`DELETE FROM database_dumps WHERE job_id = ?`, jobID); err != nil {
		return err
	}
	for _, d := range in.Dumps {
		id := d.ID
		if _, ok := old[id]; !ok {
			id = newID()
		}
		enc := ""
		switch {
		case d.UseContainerEnv:
		case d.Password != "":
			if enc, err = seal(s.cfg.encKey, d.Password, dumpAAD(id)); err != nil {
				return err
			}
		case old[id] != "":
			enc = old[id]
		default:
			return validationError{fmt.Sprintf("%s: enter the database password (or use the container's root password)", d.Name)}
		}
		dbs, _ := json.Marshal(d.Databases)
		if _, err := tx.Exec(`INSERT INTO database_dumps (id, job_id, name, container, host, port, username, password_enc, use_container_env, databases, keep_days)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, id, jobID, d.Name, d.Container, d.Host, d.Port, d.User, enc, b2i(d.UseContainerEnv), string(dbs), *d.KeepDays); err != nil {
			return err
		}
	}
	return nil
}

// loadJobExtras returns a job's excludes and dumps; with secrets, passwords
// are decrypted (for the agent), otherwise only their presence is reported.
func (s *Server) loadJobExtras(jobID string) ([]string, []dumpView, []proto.DumpConfig, error) {
	excludes := []string{}
	var raw string
	if err := s.db.QueryRow(`SELECT excludes FROM backup_jobs WHERE id = ?`, jobID).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &excludes)
	}
	views, cfgs := []dumpView{}, []proto.DumpConfig{}
	rows, err := s.db.Query(`SELECT id, name, container, host, port, username, password_enc, use_container_env, databases, keep_days
		FROM database_dumps WHERE job_id = ? ORDER BY name`, jobID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v dumpView
		var enc, dbs string
		var env int
		if err := rows.Scan(&v.ID, &v.Name, &v.Container, &v.Host, &v.Port, &v.User, &enc, &env, &dbs, &v.KeepDays); err != nil {
			return nil, nil, nil, err
		}
		v.UseContainerEnv, v.HasPassword = env == 1, enc != ""
		v.Databases = []string{}
		_ = json.Unmarshal([]byte(dbs), &v.Databases)
		views = append(views, v)
		c := proto.DumpConfig{ID: v.ID, Name: v.Name, Container: v.Container, Host: v.Host, Port: v.Port, User: v.User,
			UseContainerEnv: v.UseContainerEnv, Databases: v.Databases, KeepDays: v.KeepDays}
		if enc != "" {
			if c.Password, err = open(s.cfg.encKey, enc, dumpAAD(v.ID)); err != nil {
				return nil, nil, nil, fmt.Errorf("dump %s: %w", v.Name, err)
			}
		}
		cfgs = append(cfgs, c)
	}
	return excludes, views, cfgs, rows.Err()
}

// ---- Test a database connection from an agent -------------------------------------------

// POST /api/dumps/test {agent_id, job_id?, dump}
// A blank password with an existing dump id (and job id) uses the stored one.
func (s *Server) testDump(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentID string    `json:"agent_id"`
		JobID   string    `json:"job_id"`
		Dump    dumpInput `json:"dump"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Dump.Name == "" {
		in.Dump.Name = "test"
	}
	if err := in.Dump.validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	d := in.Dump
	cfg := proto.DumpConfig{Name: d.Name, Container: d.Container, Host: d.Host, Port: d.Port, User: d.User, Password: d.Password,
		UseContainerEnv: d.UseContainerEnv, Databases: d.Databases}
	if cfg.Password == "" && !cfg.UseContainerEnv && d.ID != "" && in.JobID != "" {
		var enc string
		if s.db.QueryRow(`SELECT password_enc FROM database_dumps WHERE id = ? AND job_id = ?`, d.ID, in.JobID).Scan(&enc) == nil && enc != "" {
			cfg.Password, _ = open(s.cfg.encKey, enc, dumpAAD(d.ID))
		}
	}
	if cfg.Password == "" && !cfg.UseContainerEnv {
		writeErr(w, 400, "enter the database password to test")
		return
	}
	if in.AgentID == "" {
		writeErr(w, 400, "agent_id is required: the database is reached from that machine")
		return
	}
	raw, err := s.hub.Call(r.Context(), in.AgentID, proto.MsgTestDump, cfg, 60*time.Second)
	if err != nil {
		writeErr(w, agentCallStatus(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
