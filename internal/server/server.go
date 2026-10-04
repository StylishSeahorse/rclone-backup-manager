package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/stylishseahorse/rclone-backup-manager/web"
)

type Server struct {
	cfg     *Config
	db      *sql.DB
	hub     *Hub
	limiter *loginLimiter
	log     *log.Logger
}

func New(cfg *Config) (*Server, error) {
	db, err := openDB(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, db: db, hub: NewHub(), limiter: newLoginLimiter(), log: log.New(os.Stderr, "[server] ", log.LstdFlags)}
	if err := s.bootstrap(); err != nil {
		return nil, err
	}
	return s, nil
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: the agent WebSocket is long-lived.
	}
	go s.janitor(ctx)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Printf("dashboard listening on %s", s.cfg.Listen)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutCtx)
		_ = s.db.Close()
		return err
	}
}

func (s *Server) janitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reapStaleRuns(10 * time.Minute)
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	admin := s.requireAdmin
	agent := s.requireAgent

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })

	// Admin / browser API
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", admin(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) }))

	mux.HandleFunc("GET /api/agents", admin(s.listAgents))
	mux.HandleFunc("POST /api/agents", admin(s.handleCreateAgent))
	mux.HandleFunc("DELETE /api/agents/{id}", admin(s.deleteAgent))
	mux.HandleFunc("POST /api/agents/{id}/rotate-key", admin(s.rotateAgentKey))
	mux.HandleFunc("GET /api/agents/{id}/browse", admin(s.browseAgent))

	mux.HandleFunc("GET /api/credentials", admin(s.listCredentials))
	mux.HandleFunc("POST /api/credentials", admin(s.createCredential))
	mux.HandleFunc("PUT /api/credentials/{id}", admin(s.updateCredential))
	mux.HandleFunc("DELETE /api/credentials/{id}", admin(s.deleteCredential))

	mux.HandleFunc("GET /api/jobs", admin(s.listJobs))
	mux.HandleFunc("POST /api/jobs", admin(s.createJob))
	mux.HandleFunc("PUT /api/jobs/{id}", admin(s.updateJob))
	mux.HandleFunc("DELETE /api/jobs/{id}", admin(s.deleteJob))
	mux.HandleFunc("POST /api/jobs/{id}/run", admin(s.runJobNow))

	mux.HandleFunc("GET /api/runs", admin(s.listRuns))
	mux.HandleFunc("GET /api/runs/{id}", admin(s.getRun))
	mux.HandleFunc("GET /api/runs/{id}/logs", admin(s.runLogs))
	mux.HandleFunc("POST /api/runs/{id}/cancel", admin(s.cancelRun))

	// Agent API (API-key authenticated)
	mux.HandleFunc("GET /api/agent/ws", agent(s.agentWS))
	mux.HandleFunc("GET /api/agent/config", agent(s.agentConfig))
	mux.HandleFunc("POST /api/agent/runs", agent(s.agentStartRun))
	mux.HandleFunc("POST /api/agent/runs/{id}/logs", agent(s.agentRunLogs))
	mux.HandleFunc("POST /api/agent/runs/{id}/finish", agent(s.agentFinishRun))

	// Embedded UI
	static, _ := fs.Sub(web.Static, "static")
	mux.Handle("/", http.FileServerFS(static))

	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes a size-limited, strictly-typed body. On failure it writes
// the 400 itself and returns false.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}

func dbErr(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if strings.Contains(err.Error(), "UNIQUE constraint") {
		writeErr(w, http.StatusConflict, "name already exists")
		return
	}
	if strings.Contains(err.Error(), "FOREIGN KEY constraint") {
		writeErr(w, http.StatusConflict, "still referenced by other records")
		return
	}
	log.Printf("db error: %v", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}
