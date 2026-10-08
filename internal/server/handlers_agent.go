package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// agentWS upgrades an authenticated agent request into the control tunnel.
// The agent initiates the connection, so no inbound port is needed on the host.
func (s *Server) agentWS(w http.ResponseWriter, r *http.Request) {
	a := agentFrom(r)
	ws, err := websocket.Accept(w, r, nil) // rejects cross-origin browser upgrades by default
	if err != nil {
		return
	}
	ws.SetReadLimit(8 << 20) // browse results for big directories can be large
	c := newAgentConn(a.ID, ws)
	s.hub.register(c)
	s.log.Printf("agent %q connected", a.Name)
	defer func() {
		s.hub.unregister(c)
		_, _ = s.db.Exec(`UPDATE agents SET last_seen_at = ? WHERE id = ?`, now(), a.ID)
		s.log.Printf("agent %q disconnected", a.Name)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() { <-c.done; cancel() }()

	// writer: the only goroutine that writes frames
	go func() {
		for {
			select {
			case env := <-c.send:
				wctx, wc := context.WithTimeout(ctx, 15*time.Second)
				b, _ := json.Marshal(env)
				err := ws.Write(wctx, websocket.MessageText, b)
				wc()
				if err != nil {
					c.close(websocket.StatusInternalError, "write failed")
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// keepalive: detects half-open TCP connections behind NAT
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				pctx, pc := context.WithTimeout(ctx, 10*time.Second)
				err := ws.Ping(pctx)
				pc()
				if err != nil {
					c.close(websocket.StatusGoingAway, "ping timeout")
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var env proto.Envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case proto.MsgHello:
			var h proto.Hello
			if json.Unmarshal(env.Payload, &h) != nil {
				continue
			}
			roots, _ := json.Marshal(h.BrowseRoots)
			_, _ = s.db.Exec(`UPDATE agents SET hostname=?, version=?, os=?, rclone_ver=?, browse_roots=?, last_seen_at=? WHERE id=?`,
				clip(h.Hostname, 255), clip(h.Version, 64), clip(h.OS+"/"+h.Arch, 64), clip(h.RcloneVer, 64), string(roots), now(), a.ID)
			if h.ConfigRev != s.rev(a.ID) {
				s.hub.Notify(a.ID) // agent's copy is stale (or empty): tell it to fetch
			}
		case proto.MsgPing:
			_, _ = s.db.Exec(`UPDATE agents SET last_seen_at = ? WHERE id = ?`, now(), a.ID)
		case proto.MsgResult:
			c.deliver(env)
		}
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// wasabiEndpoint maps a Wasabi region to its S3 endpoint host.
func wasabiEndpoint(region string) string {
	if region == "us-east-1" {
		return "s3.wasabisys.com"
	}
	return "s3." + region + ".wasabisys.com"
}

// agentConfig returns the agent's full assignment, including decrypted Wasabi
// credentials. It is only reachable with that agent's API key, and the agent
// keeps the result in memory only.
func (s *Server) agentConfig(w http.ResponseWriter, r *http.Request) {
	a := agentFrom(r)
	rev := s.rev(a.ID)
	cfg := proto.AgentConfig{Revision: rev, AgentID: a.ID, Name: a.Name, Jobs: []proto.JobConfig{}}

	rows, err := s.db.Query(`SELECT j.id, j.name, j.enabled, j.dest_prefix, j.backup_type, j.retention_days, c.id, c.access_key, c.secret_key_enc, c.region, c.bucket, c.endpoint
		FROM backup_jobs j JOIN wasabi_credentials c ON c.id = j.credential_id WHERE j.agent_id = ? ORDER BY j.name`, a.ID)
	if err != nil {
		dbErr(w, err)
		return
	}
	type row struct {
		job    proto.JobConfig
		credID string
		enc    string
	}
	var rs []row
	for rows.Next() {
		var x row
		var en int
		if err := rows.Scan(&x.job.ID, &x.job.Name, &en, &x.job.DestPrefix, &x.job.BackupType, &x.job.Retention, &x.credID,
			&x.job.Wasabi.AccessKey, &x.enc, &x.job.Wasabi.Region, &x.job.Wasabi.Bucket, &x.job.Wasabi.Endpoint); err != nil {
			rows.Close()
			dbErr(w, err)
			return
		}
		x.job.Enabled = en == 1
		rs = append(rs, x)
	}
	rows.Close()

	for _, x := range rs {
		secret, err := open(s.cfg.encKey, x.enc, "wasabi_credentials.secret_key:"+x.credID)
		if err != nil {
			s.log.Printf("job %s: %v", x.job.ID, err)
			continue // skip the job rather than ship broken credentials
		}
		x.job.Wasabi.SecretKey = secret
		if x.job.Wasabi.Endpoint == "" {
			x.job.Wasabi.Endpoint = wasabiEndpoint(x.job.Wasabi.Region)
		}
		x.job.Paths, x.job.Schedules = []proto.PathConfig{}, []proto.ScheduleSpec{}

		pr, err := s.db.Query(`SELECT path, mode FROM backup_paths WHERE job_id = ? ORDER BY path`, x.job.ID)
		if err != nil {
			dbErr(w, err)
			return
		}
		for pr.Next() {
			var p proto.PathConfig
			if pr.Scan(&p.Path, &p.Mode) == nil {
				x.job.Paths = append(x.job.Paths, p)
			}
		}
		pr.Close()

		sr, err := s.db.Query(`SELECT id, cron_expr, timezone FROM schedules WHERE job_id = ? AND enabled = 1`, x.job.ID)
		if err != nil {
			dbErr(w, err)
			return
		}
		for sr.Next() {
			var sc proto.ScheduleSpec
			if sr.Scan(&sc.ID, &sc.Cron, &sc.Timezone) == nil {
				x.job.Schedules = append(x.job.Schedules, sc)
			}
		}
		sr.Close()
		ex, _, dumps, err := s.loadJobExtras(x.job.ID)
		if err != nil {
			s.log.Printf("job %s: %v", x.job.ID, err)
			continue // never ship a job with broken credentials
		}
		x.job.Excludes, x.job.Dumps = ex, dumps
		cfg.Jobs = append(cfg.Jobs, x.job)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, cfg)
}

// ---- run reporting --------------------------------------------------------------

func (s *Server) agentStartRun(w http.ResponseWriter, r *http.Request) {
	a := agentFrom(r)
	var in proto.StartRunRequest
	if !readJSON(w, r, &in) {
		return
	}
	switch in.Trigger {
	case "schedule", "manual", proto.ModeDryRun, proto.ModeVerify:
	default:
		writeErr(w, 400, "invalid trigger")
		return
	}
	var owner string
	if err := s.db.QueryRow(`SELECT agent_id FROM backup_jobs WHERE id = ?`, in.JobID).Scan(&owner); err != nil || owner != a.ID {
		writeErr(w, http.StatusNotFound, "unknown job") // don't reveal other agents' job ids
		return
	}
	id := newID()
	if _, err := s.db.Exec(`INSERT INTO runs (id, job_id, agent_id, trigger, status, started_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		id, in.JobID, a.ID, in.Trigger, proto.StatusRunning, now(), now()); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, proto.StartRunResponse{RunID: id})
}

// ownsRun verifies the run belongs to the calling agent.
func (s *Server) ownsRun(a agentIdentity, runID string) bool {
	var owner string
	err := s.db.QueryRow(`SELECT agent_id FROM runs WHERE id = ?`, runID).Scan(&owner)
	return err == nil && owner == a.ID
}

const (
	maxLineLen    = 8192
	maxBatchLines = 2000
	maxLogSeq     = 500_000 // hard cap per run so a chatty job can't fill the disk
)

func (s *Server) agentRunLogs(w http.ResponseWriter, r *http.Request) {
	a, id := agentFrom(r), r.PathValue("id")
	if !s.ownsRun(a, id) {
		writeErr(w, 404, "unknown run")
		return
	}
	var in proto.LogBatch
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Lines) > maxBatchLines {
		writeErr(w, 400, "batch too large")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		dbErr(w, err)
		return
	}
	defer tx.Rollback()
	for _, l := range in.Lines {
		if l.Seq <= 0 || l.Seq > maxLogSeq {
			continue
		}
		stream := l.Stream
		if stream != "stdout" && stream != "stderr" && stream != "agent" {
			stream = "stdout"
		}
		// (run_id, seq) is the key, so re-sent batches are harmless.
		if _, err := tx.Exec(`INSERT INTO run_logs (run_id, seq, ts, stream, line) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`,
			id, l.Seq, l.TS, stream, clip(strings.ToValidUTF8(l.Line, "?"), maxLineLen)); err != nil {
			dbErr(w, err)
			return
		}
	}
	if _, err := tx.Exec(`UPDATE runs SET updated_at = ? WHERE id = ? AND status = 'running'`, now(), id); err != nil {
		dbErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) agentFinishRun(w http.ResponseWriter, r *http.Request) {
	a, id := agentFrom(r), r.PathValue("id")
	var in proto.FinishRunRequest
	if !readJSON(w, r, &in) {
		return
	}
	switch in.Status {
	case proto.StatusSuccess, proto.StatusFailed, proto.StatusCancelled:
	default:
		writeErr(w, 400, "invalid status")
		return
	}
	st := in.RunStats
	if st.Bytes < 0 || st.Transferred < 0 || st.Deleted < 0 || st.Versioned < 0 || st.Errors < 0 {
		writeErr(w, 400, "invalid stats")
		return
	}
	res, err := s.db.Exec(`UPDATE runs SET status = ?, exit_code = ?, summary = ?, finished_at = ?, updated_at = ?,
		bytes = ?, files_transferred = ?, files_deleted = ?, files_versioned = ?, errors = ?
		WHERE id = ? AND agent_id = ? AND status = 'running'`, in.Status, in.ExitCode, clip(in.Summary, 500), now(), now(),
		st.Bytes, st.Transferred, st.Deleted, st.Versioned, st.Errors, id, a.ID)
	if err != nil {
		dbErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already finished (idempotent retry) or reaped as stale, or not ours.
		var st string
		if err := s.db.QueryRow(`SELECT status FROM runs WHERE id = ? AND agent_id = ?`, id, a.ID).Scan(&st); err == sql.ErrNoRows {
			writeErr(w, 404, "unknown run")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
