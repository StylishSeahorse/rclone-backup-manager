package server

import (
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite" // pure-Go driver: keeps the server binary CGO-free
)

//go:embed schema.sql
var schemaSQL string

// openRaw opens the SQLite file without applying the schema.
func openRaw(dataDir string) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	return sql.Open("sqlite", "file:"+filepath.Join(dataDir, "dashboard.db")+"?"+q.Encode())
}

func openDB(dataDir string) (*sql.DB, error) {
	db, err := openRaw(dataDir)
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; a small pool avoids "database is locked".
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schemaSQL); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// migrate upgrades databases created by older versions. CREATE TABLE IF NOT
// EXISTS never adds columns, so each column added later is added here.
func migrate(db *sql.DB) error {
	has := func(table, col string) bool {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n)
		return n > 0
	}
	if !has("backup_jobs", "backup_type") {
		stmts := []string{
			`ALTER TABLE backup_jobs ADD COLUMN backup_type TEXT NOT NULL DEFAULT 'incremental' CHECK (backup_type IN ('incremental', 'sync'))`,
			`ALTER TABLE backup_jobs ADD COLUMN retention_days INTEGER NOT NULL DEFAULT 30`,
			// Jobs that mirrored with per-path "sync" keep that behaviour.
			`UPDATE backup_jobs SET backup_type = 'sync' WHERE id IN (SELECT job_id FROM backup_paths WHERE mode = 'sync')`,
		}
		for _, q := range stmts {
			if _, err := db.Exec(q); err != nil {
				return err
			}
		}
	}
	return nil
}

func now() int64 { return time.Now().Unix() }

// bootstrap creates the first admin user and (optionally) a pre-enrolled agent.
func (s *Server) bootstrap() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		pw := s.cfg.AdminPassword
		generated := false
		if pw == "" {
			k, err := newAgentKey()
			if err != nil {
				return err
			}
			pw, generated = k[4:20], true
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES (?,?,?,?)`,
			newID(), s.cfg.AdminUser, string(hash), now()); err != nil {
			return err
		}
		if generated {
			s.log.Printf("created admin user %q with GENERATED password: %s  (change it via ADMIN_PASSWORD on a fresh volume)", s.cfg.AdminUser, pw)
		} else {
			s.log.Printf("created admin user %q", s.cfg.AdminUser)
		}
	}

	if k := s.cfg.BootstrapAgentKey; k != "" {
		var exists int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE api_key_hash = ?`, hashKey(k)).Scan(&exists)
		if exists == 0 {
			if _, err := s.createAgent(s.cfg.BootstrapAgentName, k); err != nil {
				s.log.Printf("bootstrap agent %q not created: %v", s.cfg.BootstrapAgentName, err)
			} else {
				s.log.Printf("pre-enrolled agent %q from BOOTSTRAP_AGENT_KEY", s.cfg.BootstrapAgentName)
			}
		}
	}
	return nil
}

// bumpRev marks an agent's config as changed. Returns the new revision.
func (s *Server) bumpRev(agentID string) {
	_, _ = s.db.Exec(`INSERT INTO agent_config_rev (agent_id, rev) VALUES (?, 2)
		ON CONFLICT(agent_id) DO UPDATE SET rev = rev + 1`, agentID)
	s.hub.Notify(agentID)
}

func (s *Server) rev(agentID string) int64 {
	var r int64 = 1
	_ = s.db.QueryRow(`SELECT rev FROM agent_config_rev WHERE agent_id = ?`, agentID).Scan(&r)
	return r
}

// reapStaleRuns fails runs that stopped reporting (agent crashed / lost
// network), so the history never shows a run "running" forever.
func (s *Server) reapStaleRuns(olderThan time.Duration) {
	cutoff := time.Now().Add(-olderThan).Unix()
	res, err := s.db.Exec(`UPDATE runs SET status = 'failed', finished_at = ?, updated_at = ?,
		summary = CASE WHEN summary = '' THEN 'lost contact with agent' ELSE summary END
		WHERE status = 'running' AND updated_at < ?`, now(), now(), cutoff)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			s.log.Printf("marked %d stale run(s) as failed", n)
		}
	}
}
