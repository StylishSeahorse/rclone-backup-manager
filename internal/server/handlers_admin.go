package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// ============================== Agents ========================================

type agentView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Hostname    string   `json:"hostname"`
	Version     string   `json:"version"`
	OS          string   `json:"os"`
	RcloneVer   string   `json:"rclone_version"`
	BrowseRoots []string `json:"browse_roots"`
	LastSeenAt  int64    `json:"last_seen_at"`
	CreatedAt   int64    `json:"created_at"`
	Online      bool     `json:"online"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)

func (s *Server) createAgent(name, key string) (string, error) {
	id := newID()
	_, err := s.db.Exec(`INSERT INTO agents (id, name, api_key_hash, created_at) VALUES (?,?,?,?)`, id, name, hashKey(key), now())
	return id, err
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT id, name, hostname, version, os, rclone_ver, browse_roots, last_seen_at, created_at FROM agents ORDER BY name`)
	if err != nil {
		dbErr(w, err)
		return
	}
	defer rows.Close()
	out := []agentView{}
	for rows.Next() {
		var a agentView
		var roots string
		if err := rows.Scan(&a.ID, &a.Name, &a.Hostname, &a.Version, &a.OS, &a.RcloneVer, &roots, &a.LastSeenAt, &a.CreatedAt); err != nil {
			dbErr(w, err)
			return
		}
		a.BrowseRoots = []string{}
		_ = json.Unmarshal([]byte(roots), &a.BrowseRoots)
		a.Online = s.hub.Online(a.ID)
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !nameRe.MatchString(in.Name) {
		writeErr(w, http.StatusBadRequest, "name must be 1-64 chars: letters, digits, space, . _ -")
		return
	}
	key, err := newAgentKey()
	if err != nil {
		writeErr(w, 500, "internal error")
		return
	}
	id, err := s.createAgent(in.Name, key)
	if err != nil {
		dbErr(w, err)
		return
	}
	// The key is returned exactly once; only its hash is stored.
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": in.Name, "api_key": key})
}

func (s *Server) rotateAgentKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key, err := newAgentKey()
	if err != nil {
		writeErr(w, 500, "internal error")
		return
	}
	res, err := s.db.Exec(`UPDATE agents SET api_key_hash = ? WHERE id = ?`, hashKey(key), id)
	if err != nil {
		dbErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, 404, "not found")
		return
	}
	s.hub.Disconnect(id) // the old key must stop working immediately
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "api_key": key})
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		dbErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, 404, "not found")
		return
	}
	s.hub.Disconnect(id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// browseAgent is the dashboard's window into an agent's filesystem: it relays
// the request through the agent's tunnel and returns the agent's JSON listing.
func (s *Server) browseAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := r.URL.Query().Get("path")
	if len(p) > 4096 || strings.ContainsRune(p, 0) {
		writeErr(w, 400, "invalid path")
		return
	}
	raw, err := s.hub.Call(r.Context(), id, proto.MsgBrowse, proto.BrowseRequest{Path: p}, 20*time.Second)
	if err != nil {
		var ae *AgentError
		switch {
		case errors.Is(err, ErrAgentOffline):
			writeErr(w, http.StatusServiceUnavailable, "agent is offline")
		case errors.As(err, &ae):
			writeErr(w, http.StatusBadRequest, ae.Msg)
		default:
			writeErr(w, http.StatusGatewayTimeout, err.Error())
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// ============================ Credentials =====================================

type credView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AccessKey string `json:"access_key"`
	HasSecret bool   `json:"has_secret"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Endpoint  string `json:"endpoint"`
	CreatedAt int64  `json:"created_at"`
}

type credInput struct {
	Name      string `json:"name"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Endpoint  string `json:"endpoint"`
}

var (
	regionRe   = regexp.MustCompile(`^[a-z0-9-]{2,32}$`)
	bucketRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	endpointRe = regexp.MustCompile(`^(https?://)?[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)
	secretRe   = regexp.MustCompile(`^[\x21-\x7e]{4,256}$`) // printable ASCII, no whitespace/control
)

func (in *credInput) validate(secretRequired bool) error {
	in.Name, in.AccessKey, in.Region, in.Bucket, in.Endpoint =
		strings.TrimSpace(in.Name), strings.TrimSpace(in.AccessKey), strings.TrimSpace(in.Region),
		strings.TrimSpace(in.Bucket), strings.TrimSpace(in.Endpoint)
	switch {
	case !nameRe.MatchString(in.Name):
		return errors.New("invalid name")
	case !secretRe.MatchString(in.AccessKey):
		return errors.New("invalid access key")
	case secretRequired && !secretRe.MatchString(in.SecretKey):
		return errors.New("invalid secret key")
	case !secretRequired && in.SecretKey != "" && !secretRe.MatchString(in.SecretKey):
		return errors.New("invalid secret key")
	case !regionRe.MatchString(in.Region):
		return errors.New("invalid region (e.g. us-east-1, eu-central-1)")
	case !bucketRe.MatchString(in.Bucket):
		return errors.New("invalid bucket name")
	case in.Endpoint != "" && !endpointRe.MatchString(in.Endpoint):
		return errors.New("invalid endpoint (host or http(s)://host[:port])")
	}
	return nil
}

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT id, name, access_key, region, bucket, endpoint, created_at FROM wasabi_credentials ORDER BY name`)
	if err != nil {
		dbErr(w, err)
		return
	}
	defer rows.Close()
	out := []credView{}
	for rows.Next() {
		c := credView{HasSecret: true}
		if err := rows.Scan(&c.ID, &c.Name, &c.AccessKey, &c.Region, &c.Bucket, &c.Endpoint, &c.CreatedAt); err != nil {
			dbErr(w, err)
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createCredential(w http.ResponseWriter, r *http.Request) {
	var in credInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.validate(true); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := newID()
	enc, err := seal(s.cfg.encKey, in.SecretKey, "wasabi_credentials.secret_key:"+id)
	if err != nil {
		writeErr(w, 500, "internal error")
		return
	}
	_, err = s.db.Exec(`INSERT INTO wasabi_credentials (id, name, access_key, secret_key_enc, region, bucket, endpoint, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		id, in.Name, in.AccessKey, enc, in.Region, in.Bucket, in.Endpoint, now())
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, credView{ID: id, Name: in.Name, AccessKey: in.AccessKey, HasSecret: true, Region: in.Region, Bucket: in.Bucket, Endpoint: in.Endpoint, CreatedAt: now()})
}

func (s *Server) updateCredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in credInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.validate(false); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var res sql.Result
	var err error
	if in.SecretKey == "" { // blank secret = keep the stored one
		res, err = s.db.Exec(`UPDATE wasabi_credentials SET name=?, access_key=?, region=?, bucket=?, endpoint=? WHERE id=?`,
			in.Name, in.AccessKey, in.Region, in.Bucket, in.Endpoint, id)
	} else {
		var enc string
		if enc, err = seal(s.cfg.encKey, in.SecretKey, "wasabi_credentials.secret_key:"+id); err != nil {
			writeErr(w, 500, "internal error")
			return
		}
		res, err = s.db.Exec(`UPDATE wasabi_credentials SET name=?, access_key=?, secret_key_enc=?, region=?, bucket=?, endpoint=? WHERE id=?`,
			in.Name, in.AccessKey, enc, in.Region, in.Bucket, in.Endpoint, id)
	}
	if err != nil {
		dbErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, 404, "not found")
		return
	}
	s.bumpAgentsUsingCredential(id)
	writeJSON(w, http.StatusOK, credView{ID: id, Name: in.Name, AccessKey: in.AccessKey, HasSecret: true, Region: in.Region, Bucket: in.Bucket, Endpoint: in.Endpoint})
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	res, err := s.db.Exec(`DELETE FROM wasabi_credentials WHERE id = ?`, r.PathValue("id"))
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			writeErr(w, http.StatusConflict, "credential is used by a backup job")
			return
		}
		dbErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) bumpAgentsUsingCredential(credID string) {
	rows, err := s.db.Query(`SELECT DISTINCT agent_id FROM backup_jobs WHERE credential_id = ?`, credID)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		s.bumpRev(id)
	}
}

// ================================ Jobs ========================================

type jobView struct {
	ID            string             `json:"id"`
	AgentID       string             `json:"agent_id"`
	AgentName     string             `json:"agent_name"`
	CredentialID  string             `json:"credential_id"`
	Name          string             `json:"name"`
	DestPrefix    string             `json:"dest_prefix"`
	Enabled       bool               `json:"enabled"`
	BackupType    string             `json:"backup_type"`
	RetentionDays int                `json:"retention_days"`
	Paths         []proto.PathConfig `json:"paths"`
	Schedules     []scheduleView     `json:"schedules"`
	LastRun       *runView           `json:"last_run"`
	NextRun       *int64             `json:"next_run"` // unix seconds; nil if disabled/unscheduled
}

type scheduleView struct {
	CronExpr string `json:"cron_expr"`
	Timezone string `json:"timezone"`
	Enabled  bool   `json:"enabled"`
}

type jobInput struct {
	AgentID       string             `json:"agent_id"`
	CredentialID  string             `json:"credential_id"`
	Name          string             `json:"name"`
	DestPrefix    string             `json:"dest_prefix"`
	Enabled       bool               `json:"enabled"`
	BackupType    string             `json:"backup_type"`    // "incremental" (default) | "sync"
	RetentionDays *int               `json:"retention_days"` // incremental: days to keep versions; 0 = forever; default 30
	Paths         []proto.PathConfig `json:"paths"`
	Schedules     []scheduleView     `json:"schedules"`
}

var prefixRe = regexp.MustCompile(`^[A-Za-z0-9._\- /]{0,200}$`)

func (in *jobInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if !nameRe.MatchString(in.Name) {
		return errors.New("invalid job name")
	}
	if in.CredentialID == "" {
		return errors.New("credential is required")
	}
	// Normalise the bucket key prefix; refuse tricks like ".." segments.
	in.DestPrefix = strings.Trim(strings.TrimSpace(in.DestPrefix), "/")
	if !prefixRe.MatchString(in.DestPrefix) {
		return errors.New("destination prefix may only contain letters, digits, space and . _ - /")
	}
	for _, seg := range strings.Split(in.DestPrefix, "/") {
		if seg == ".." || (seg == "" && in.DestPrefix != "") {
			return errors.New("invalid destination prefix")
		}
	}
	switch in.BackupType {
	case "":
		in.BackupType = proto.BackupIncremental
	case proto.BackupIncremental, proto.BackupSync:
	default:
		return errors.New("backup type must be incremental or sync")
	}
	if in.RetentionDays == nil {
		d := 30
		in.RetentionDays = &d
	}
	if *in.RetentionDays < 0 || *in.RetentionDays > 3650 {
		return errors.New("keep versions for 0 (forever) to 3650 days")
	}
	if len(in.Paths) == 0 {
		return errors.New("select at least one path to back up")
	}
	if len(in.Paths) > 500 {
		return errors.New("too many paths (max 500)")
	}
	seen := map[string]bool{}
	for i := range in.Paths {
		p := &in.Paths[i]
		if p.Mode == "" {
			p.Mode = "copy" // legacy per-path field; the job's backup_type decides
		}
		if p.Mode != "copy" && p.Mode != "sync" {
			return fmt.Errorf("invalid mode %q for %s", p.Mode, p.Path)
		}
		if !path.IsAbs(p.Path) || path.Clean(p.Path) != p.Path || strings.ContainsAny(p.Path, "\x00\r\n") || len(p.Path) > 4096 {
			return fmt.Errorf("invalid path %q (must be an absolute, clean path)", p.Path)
		}
		if p.Path == "/" {
			return errors.New("refusing to back up the filesystem root; pick specific directories")
		}
		if p.Path == "/.versions" || strings.HasPrefix(p.Path, "/.versions/") {
			return errors.New("/.versions is reserved for incremental version history")
		}
		if seen[p.Path] {
			return fmt.Errorf("duplicate path %s", p.Path)
		}
		seen[p.Path] = true
	}
	if len(in.Schedules) > 20 {
		return errors.New("too many schedules (max 20)")
	}
	for i := range in.Schedules {
		sc := &in.Schedules[i]
		if sc.Timezone == "" {
			sc.Timezone = "UTC"
		}
		if _, err := time.LoadLocation(sc.Timezone); err != nil {
			return fmt.Errorf("unknown timezone %q", sc.Timezone)
		}
		if err := proto.ValidateSchedule(sc.CronExpr, sc.Timezone); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) loadJob(id string) (*jobView, error) {
	rows, err := s.db.Query(jobSelect+` WHERE j.id = ?`, id)
	if err != nil {
		return nil, err
	}
	jobs, err := s.scanJobs(rows)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, sql.ErrNoRows
	}
	return &jobs[0], nil
}

const jobSelect = `SELECT j.id, j.agent_id, a.name, j.credential_id, j.name, j.dest_prefix, j.enabled, j.backup_type, j.retention_days
	FROM backup_jobs j JOIN agents a ON a.id = j.agent_id`

func (s *Server) scanJobs(rows *sql.Rows) ([]jobView, error) {
	defer rows.Close()
	jobs := []jobView{}
	for rows.Next() {
		var j jobView
		var en int
		if err := rows.Scan(&j.ID, &j.AgentID, &j.AgentName, &j.CredentialID, &j.Name, &j.DestPrefix, &en, &j.BackupType, &j.RetentionDays); err != nil {
			return nil, err
		}
		j.Enabled = en == 1
		j.Paths, j.Schedules = []proto.PathConfig{}, []scheduleView{}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range jobs {
		j := &jobs[i]
		pr, err := s.db.Query(`SELECT path, mode FROM backup_paths WHERE job_id = ? ORDER BY path`, j.ID)
		if err != nil {
			return nil, err
		}
		for pr.Next() {
			var p proto.PathConfig
			if err := pr.Scan(&p.Path, &p.Mode); err != nil {
				pr.Close()
				return nil, err
			}
			j.Paths = append(j.Paths, p)
		}
		pr.Close()
		sr, err := s.db.Query(`SELECT cron_expr, timezone, enabled FROM schedules WHERE job_id = ? ORDER BY id`, j.ID)
		if err != nil {
			return nil, err
		}
		for sr.Next() {
			var sc scheduleView
			var en int
			if err := sr.Scan(&sc.CronExpr, &sc.Timezone, &en); err != nil {
				sr.Close()
				return nil, err
			}
			sc.Enabled = en == 1
			j.Schedules = append(j.Schedules, sc)
		}
		sr.Close()
		j.LastRun = s.lastRun(j.ID)
		if j.Enabled {
			j.NextRun = nextRun(j.Schedules, time.Now())
		}
	}
	return jobs, nil
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	q, args := jobSelect, []any{}
	if a := r.URL.Query().Get("agent_id"); a != "" {
		q += ` WHERE j.agent_id = ?`
		args = append(args, a)
	}
	rows, err := s.db.Query(q+` ORDER BY a.name, j.name`, args...)
	if err != nil {
		dbErr(w, err)
		return
	}
	jobs, err := s.scanJobs(rows)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) writeJobChildren(tx *sql.Tx, jobID string, in *jobInput) error {
	if _, err := tx.Exec(`DELETE FROM backup_paths WHERE job_id = ?`, jobID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM schedules WHERE job_id = ?`, jobID); err != nil {
		return err
	}
	for _, p := range in.Paths {
		if _, err := tx.Exec(`INSERT INTO backup_paths (id, job_id, path, mode) VALUES (?,?,?,?)`, newID(), jobID, p.Path, p.Mode); err != nil {
			return err
		}
	}
	for _, sc := range in.Schedules {
		if _, err := tx.Exec(`INSERT INTO schedules (id, job_id, cron_expr, timezone, enabled) VALUES (?,?,?,?,?)`,
			newID(), jobID, strings.TrimSpace(sc.CronExpr), sc.Timezone, b2i(sc.Enabled)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var in jobInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := newID()
	tx, err := s.db.Begin()
	if err != nil {
		dbErr(w, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO backup_jobs (id, agent_id, credential_id, name, dest_prefix, enabled, backup_type, retention_days, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		id, in.AgentID, in.CredentialID, in.Name, in.DestPrefix, b2i(in.Enabled), in.BackupType, *in.RetentionDays, now(), now()); err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			writeErr(w, 400, "unknown agent or credential")
			return
		}
		dbErr(w, err)
		return
	}
	if err := s.writeJobChildren(tx, id, &in); err != nil {
		dbErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		dbErr(w, err)
		return
	}
	s.bumpRev(in.AgentID)
	j, err := s.loadJob(id)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, j)
}

func (s *Server) updateJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in jobInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var agentID string
	if err := s.db.QueryRow(`SELECT agent_id FROM backup_jobs WHERE id = ?`, id).Scan(&agentID); err != nil {
		dbErr(w, err)
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		dbErr(w, err)
		return
	}
	defer tx.Rollback()
	// agent_id is immutable: moving a job between machines would silently
	// retarget paths that only make sense on the original host.
	if _, err := tx.Exec(`UPDATE backup_jobs SET credential_id=?, name=?, dest_prefix=?, enabled=?, backup_type=?, retention_days=?, updated_at=? WHERE id=?`,
		in.CredentialID, in.Name, in.DestPrefix, b2i(in.Enabled), in.BackupType, *in.RetentionDays, now(), id); err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			writeErr(w, 400, "unknown credential")
			return
		}
		dbErr(w, err)
		return
	}
	if err := s.writeJobChildren(tx, id, &in); err != nil {
		dbErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		dbErr(w, err)
		return
	}
	s.bumpRev(agentID)
	j, err := s.loadJob(id)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var agentID string
	if err := s.db.QueryRow(`SELECT agent_id FROM backup_jobs WHERE id = ?`, id).Scan(&agentID); err != nil {
		dbErr(w, err)
		return
	}
	if _, err := s.db.Exec(`DELETE FROM backup_jobs WHERE id = ?`, id); err != nil {
		dbErr(w, err)
		return
	}
	s.bumpRev(agentID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) runJobNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var agentID string
	if err := s.db.QueryRow(`SELECT agent_id FROM backup_jobs WHERE id = ?`, id).Scan(&agentID); err != nil {
		dbErr(w, err)
		return
	}
	// Optional body {"mode": "dry-run" | "verify"}; empty means a real backup.
	var in struct {
		Mode string `json:"mode"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	switch in.Mode {
	case "", proto.ModeBackup, proto.ModeDryRun, proto.ModeVerify:
	default:
		writeErr(w, 400, "mode must be backup, dry-run or verify")
		return
	}
	if err := s.hub.Send(agentID, proto.MsgRunNow, proto.RunNowRequest{JobID: id, Mode: in.Mode}); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}

// ================================ Runs ========================================

type runView struct {
	ID         string `json:"id"`
	JobID      string `json:"job_id"`
	JobName    string `json:"job_name"`
	AgentName  string `json:"agent_name"`
	Trigger    string `json:"trigger"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exit_code"`
	Summary    string `json:"summary"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt *int64 `json:"finished_at"`
	proto.RunStats
}

const runSelect = `SELECT r.id, r.job_id, j.name, a.name, r.trigger, r.status, r.exit_code, r.summary, r.started_at, r.finished_at,
	r.bytes, r.files_transferred, r.files_deleted, r.files_versioned, r.errors
	FROM runs r JOIN backup_jobs j ON j.id = r.job_id JOIN agents a ON a.id = r.agent_id`

func scanRun(sc interface{ Scan(...any) error }) (*runView, error) {
	var v runView
	var ec sql.NullInt64
	var fin sql.NullInt64
	if err := sc.Scan(&v.ID, &v.JobID, &v.JobName, &v.AgentName, &v.Trigger, &v.Status, &ec, &v.Summary, &v.StartedAt, &fin,
		&v.Bytes, &v.Transferred, &v.Deleted, &v.Versioned, &v.Errors); err != nil {
		return nil, err
	}
	if ec.Valid {
		n := int(ec.Int64)
		v.ExitCode = &n
	}
	if fin.Valid {
		v.FinishedAt = &fin.Int64
	}
	return &v, nil
}

func (s *Server) lastRun(jobID string) *runView {
	v, err := scanRun(s.db.QueryRow(runSelect+` WHERE r.job_id = ? ORDER BY r.started_at DESC LIMIT 1`, jobID))
	if err != nil {
		return nil
	}
	return v
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	q, args := runSelect, []any{}
	var where []string
	if v := r.URL.Query().Get("job_id"); v != "" {
		where, args = append(where, "r.job_id = ?"), append(args, v)
	}
	if v := r.URL.Query().Get("agent_id"); v != "" {
		where, args = append(where, "r.agent_id = ?"), append(args, v)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	rows, err := s.db.Query(q+` ORDER BY r.started_at DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		dbErr(w, err)
		return
	}
	defer rows.Close()
	out := []*runView{}
	for rows.Next() {
		v, err := scanRun(rows)
		if err != nil {
			dbErr(w, err)
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	v, err := scanRun(s.db.QueryRow(runSelect+` WHERE r.id = ?`, r.PathValue("id")))
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// runLogs returns log lines with seq > after, so the UI can tail a live run by
// polling with the last seq it has seen.
func (s *Server) runLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	after := int64(0)
	if n, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64); err == nil {
		after = n
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM runs WHERE id = ?`, id).Scan(&status); err != nil {
		dbErr(w, err)
		return
	}
	rows, err := s.db.Query(`SELECT seq, ts, stream, line FROM run_logs WHERE run_id = ? AND seq > ? ORDER BY seq LIMIT 2000`, id, after)
	if err != nil {
		dbErr(w, err)
		return
	}
	defer rows.Close()
	lines := []proto.LogLine{}
	for rows.Next() {
		var l proto.LogLine
		if err := rows.Scan(&l.Seq, &l.TS, &l.Stream, &l.Line); err != nil {
			dbErr(w, err)
			return
		}
		lines = append(lines, l)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "lines": lines})
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var agentID, status string
	if err := s.db.QueryRow(`SELECT agent_id, status FROM runs WHERE id = ?`, id).Scan(&agentID, &status); err != nil {
		dbErr(w, err)
		return
	}
	if status != proto.StatusRunning {
		writeErr(w, http.StatusConflict, "run is not running")
		return
	}
	if err := s.hub.Send(agentID, proto.MsgCancelRun, proto.CancelRunRequest{RunID: id}); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}
