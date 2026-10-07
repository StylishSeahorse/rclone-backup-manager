package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// ---- Credential test ------------------------------------------------------------
//
// The test runs on an agent, not on the dashboard: what matters is whether the
// machine being backed up can reach Wasabi with these keys (DNS, firewall,
// proxy, clock skew), and the agent already has rclone.

type credTestInput struct {
	AgentID      string     `json:"agent_id"`      // "" = any online agent
	CredentialID string     `json:"credential_id"` // stored credential (its secret fills a blank secret_key)
	Credential   *credInput `json:"credential"`    // unsaved form values; nil = test the stored one as is
	WriteTest    bool       `json:"write_test"`
}

func (s *Server) testCredential(w http.ResponseWriter, r *http.Request) {
	var in credTestInput
	if !readJSON(w, r, &in) {
		return
	}
	cfg, err := s.resolveCredential(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID, agentName, err := s.pickAgent(in.AgentID)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	raw, err := s.hub.Call(r.Context(), agentID, proto.MsgTestCreds,
		proto.CredentialTestRequest{Wasabi: cfg, WriteTest: in.WriteTest}, 90*time.Second)
	if err != nil {
		writeErr(w, agentCallStatus(err), err.Error())
		return
	}
	var res proto.CredentialTestResult
	if err := json.Unmarshal(raw, &res); err != nil {
		writeErr(w, http.StatusBadGateway, "bad answer from agent")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agentName, "endpoint": cfg.Endpoint, "ok": res.OK, "steps": res.Steps})
}

func (s *Server) resolveCredential(in credTestInput) (proto.WasabiConfig, error) {
	var stored *proto.WasabiConfig
	if in.CredentialID != "" {
		var c proto.WasabiConfig
		var enc string
		err := s.db.QueryRow(`SELECT access_key, secret_key_enc, region, bucket, endpoint FROM wasabi_credentials WHERE id = ?`, in.CredentialID).
			Scan(&c.AccessKey, &enc, &c.Region, &c.Bucket, &c.Endpoint)
		if err != nil {
			return c, errors.New("unknown credential")
		}
		if c.SecretKey, err = open(s.cfg.encKey, enc, "wasabi_credentials.secret_key:"+in.CredentialID); err != nil {
			return c, errors.New("stored secret cannot be decrypted (SERVER_SECRET changed?)")
		}
		stored = &c
	}

	var cfg proto.WasabiConfig
	switch {
	case in.Credential != nil:
		ci := *in.Credential
		if ci.Name == "" {
			ci.Name = "test" // the form may not have a name yet
		}
		if err := ci.validate(false); err != nil {
			return cfg, err
		}
		cfg = proto.WasabiConfig{AccessKey: ci.AccessKey, SecretKey: ci.SecretKey, Region: ci.Region, Bucket: ci.Bucket, Endpoint: ci.Endpoint}
		if cfg.SecretKey == "" {
			if stored == nil {
				return cfg, errors.New("enter the secret key to test")
			}
			cfg.SecretKey = stored.SecretKey
		}
	case stored != nil:
		cfg = *stored
	default:
		return cfg, errors.New("nothing to test")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = wasabiEndpoint(cfg.Region)
	}
	return cfg, nil
}

// pickAgent returns the requested agent if it is online, or any online agent.
func (s *Server) pickAgent(id string) (string, string, error) {
	rows, err := s.db.Query(`SELECT id, name FROM agents ORDER BY name`)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		var aid, name string
		if rows.Scan(&aid, &name) != nil {
			continue
		}
		if (id == "" || id == aid) && s.hub.Online(aid) {
			return aid, name, nil
		}
	}
	if id != "" {
		return "", "", errors.New("that agent is offline")
	}
	return "", "", errors.New("no agent is online to run the test from")
}

func agentCallStatus(err error) int {
	var ae *AgentError
	switch {
	case errors.Is(err, ErrAgentOffline):
		return http.StatusServiceUnavailable
	case errors.As(err, &ae):
		return http.StatusBadRequest
	}
	return http.StatusGatewayTimeout
}

// ---- Agent connection test ----------------------------------------------------------

func (s *Server) agentStatus(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	raw, err := s.hub.Call(r.Context(), r.PathValue("id"), proto.MsgStatus, nil, 10*time.Second)
	rtt := time.Since(t0)
	if err != nil {
		writeErr(w, agentCallStatus(err), err.Error())
		return
	}
	var st proto.AgentStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		writeErr(w, http.StatusBadGateway, "bad answer from agent")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rtt_ms": rtt.Milliseconds(), "status": st})
}

// ---- Schedule preview -----------------------------------------------------------------

func (s *Server) previewSchedule(w http.ResponseWriter, r *http.Request) {
	var in scheduleView
	if !readJSON(w, r, &in) {
		return
	}
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		writeErr(w, 400, "unknown timezone "+in.Timezone)
		return
	}
	if err := proto.ValidateSchedule(in.CronExpr, in.Timezone); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	sched, _ := proto.ScheduleParser.Parse(proto.FullSpec(in.CronExpr, in.Timezone))
	next := []int64{}
	t := time.Now()
	for i := 0; i < 5; i++ {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		next = append(next, t.Unix())
	}
	writeJSON(w, http.StatusOK, map[string]any{"next": next})
}

// nextRun is the soonest upcoming fire time across a job's enabled schedules.
func nextRun(schedules []scheduleView, now time.Time) *int64 {
	var best int64
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		sched, err := proto.ScheduleParser.Parse(proto.FullSpec(sc.CronExpr, sc.Timezone))
		if err != nil {
			continue
		}
		if t := sched.Next(now); !t.IsZero() && (best == 0 || t.Unix() < best) {
			best = t.Unix()
		}
	}
	if best == 0 {
		return nil
	}
	return &best
}
