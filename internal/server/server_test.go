package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

func TestSealOpen(t *testing.T) {
	key, _ := derive([]byte(strings.Repeat("k", 32)), "test")
	enc, err := seal(key, "wasabi-secret", "col:1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "wasabi-secret") {
		t.Fatal("ciphertext contains plaintext")
	}
	if got, err := open(key, enc, "col:1"); err != nil || got != "wasabi-secret" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if _, err := open(key, enc, "col:2"); err == nil {
		t.Error("ciphertext must not decrypt under a different AAD (transplant protection)")
	}
	other, _ := derive([]byte(strings.Repeat("z", 32)), "test")
	if _, err := open(other, enc, "col:1"); err == nil {
		t.Error("ciphertext must not decrypt under a different key")
	}
	enc2, _ := seal(key, "wasabi-secret", "col:1")
	if enc == enc2 {
		t.Error("nonce reuse: two seals of the same plaintext are identical")
	}
}

type harness struct {
	t   *testing.T
	srv *Server
	ts  *httptest.Server
	cli *http.Client
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_PASSWORD", "test-password-1")
	t.Setenv("SERVER_SECRET", strings.Repeat("s", 40))
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.db.Close() })
	jar, _ := cookiejar.New(nil)
	return &harness{t: t, srv: s, ts: ts, cli: &http.Client{Jar: jar}}
}

func (h *harness) do(method, path string, body any, auth string) (int, []byte) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, rdr)
	req.Header.Set("X-Requested-With", "dashboard")
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := h.cli.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (h *harness) login() {
	h.t.Helper()
	if code, b := h.do("POST", "/api/login", map[string]string{"username": "admin", "password": "test-password-1"}, ""); code != 200 {
		h.t.Fatalf("login: %d %s", code, b)
	}
}

func TestAdminAPIRequiresAuth(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/api/agents", "/api/credentials", "/api/jobs", "/api/runs"} {
		if code, _ := h.do("GET", p, nil, ""); code != http.StatusUnauthorized {
			t.Errorf("GET %s without login = %d, want 401", p, code)
		}
	}
	// An agent key is not an admin credential.
	h.login()
	_, b := h.do("POST", "/api/agents", map[string]string{"name": "a1"}, "")
	var created struct {
		APIKey string `json:"api_key"`
	}
	_ = json.Unmarshal(b, &created)
	anon := &harness{t: t, srv: h.srv, ts: h.ts, cli: &http.Client{}}
	if code, _ := anon.do("GET", "/api/agents", nil, created.APIKey); code != http.StatusUnauthorized {
		t.Errorf("agent key accepted on admin API: %d", code)
	}
	// Writes need the CSRF header even with a valid cookie.
	req, _ := http.NewRequest("POST", h.ts.URL+"/api/agents", strings.NewReader(`{"name":"x"}`))
	resp, _ := h.cli.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("write without X-Requested-With = %d, want 403", resp.StatusCode)
	}
}

func TestCredentialSecretNeverReturnedAndEncryptedAtRest(t *testing.T) {
	h := newHarness(t)
	h.login()
	code, b := h.do("POST", "/api/credentials", map[string]string{"name": "w", "access_key": "AKIA12345678", "secret_key": "TOPSECRET-123456", "region": "us-east-1", "bucket": "bkt-1"}, "")
	if code != 201 {
		t.Fatalf("create: %d %s", code, b)
	}
	if bytes.Contains(b, []byte("TOPSECRET")) {
		t.Error("create response leaks the secret")
	}
	_, b = h.do("GET", "/api/credentials", nil, "")
	if bytes.Contains(b, []byte("TOPSECRET")) {
		t.Error("list response leaks the secret")
	}
	var enc string
	_ = h.srv.db.QueryRow(`SELECT secret_key_enc FROM wasabi_credentials`).Scan(&enc)
	if enc == "" || strings.Contains(enc, "TOPSECRET") {
		t.Errorf("secret not encrypted at rest: %q", enc)
	}
}

// fakeAgent connects like the real agent and answers browse requests.
func (h *harness) fakeAgent(key string, handle func(proto.Envelope) proto.Envelope) *websocket.Conn {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.ts.URL, "http")+"/api/agent/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key}}})
	if err != nil {
		h.t.Fatalf("agent dial: %v", err)
	}
	h.t.Cleanup(func() { ws.CloseNow() })
	hello, _ := json.Marshal(proto.Hello{Hostname: "box", Version: "t", OS: "linux", Arch: "amd64", BrowseRoots: []string{"/data"}})
	b, _ := json.Marshal(proto.Envelope{Type: proto.MsgHello, Payload: hello})
	_ = ws.Write(ctx, websocket.MessageText, b)
	go func() {
		for {
			_, data, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var env proto.Envelope
			_ = json.Unmarshal(data, &env)
			if env.ID == "" {
				continue
			}
			out, _ := json.Marshal(handle(env))
			_ = ws.Write(context.Background(), websocket.MessageText, out)
		}
	}()
	return ws
}

func TestBrowseIsRelayedThroughAgentTunnel(t *testing.T) {
	h := newHarness(t)
	h.login()
	_, b := h.do("POST", "/api/agents", map[string]string{"name": "nas"}, "")
	var ag struct{ ID, APIKey string }
	var raw map[string]string
	_ = json.Unmarshal(b, &raw)
	ag.ID, ag.APIKey = raw["id"], raw["api_key"]

	// offline first
	if code, _ := h.do("GET", "/api/agents/"+ag.ID+"/browse?path=/data", nil, ""); code != http.StatusServiceUnavailable {
		t.Errorf("offline browse = %d, want 503", code)
	}

	h.fakeAgent(ag.APIKey, func(env proto.Envelope) proto.Envelope {
		var req proto.BrowseRequest
		_ = json.Unmarshal(env.Payload, &req)
		if req.Path == "/etc" {
			return proto.Envelope{ID: env.ID, Type: proto.MsgResult, Error: "path is outside the allowed backup roots"}
		}
		res, _ := json.Marshal(proto.BrowseResult{Path: req.Path, Parent: "/", Entries: []proto.Entry{{Name: "docs", Path: req.Path + "/docs", IsDir: true}}})
		return proto.Envelope{ID: env.ID, Type: proto.MsgResult, Payload: res}
	})
	waitFor(t, func() bool { return h.srv.hub.Online(ag.ID) })

	code, b := h.do("GET", "/api/agents/"+ag.ID+"/browse?path=/data", nil, "")
	var res proto.BrowseResult
	if code != 200 || json.Unmarshal(b, &res) != nil || len(res.Entries) != 1 || res.Entries[0].Path != "/data/docs" {
		t.Fatalf("browse: %d %s", code, b)
	}
	if code, b := h.do("GET", "/api/agents/"+ag.ID+"/browse?path=/etc", nil, ""); code != 400 || !strings.Contains(string(b), "outside") {
		t.Errorf("agent-side rejection should surface as 400: %d %s", code, b)
	}
	// hello metadata was recorded
	waitFor(t, func() bool {
		var host string
		_ = h.srv.db.QueryRow(`SELECT hostname FROM agents WHERE id = ?`, ag.ID).Scan(&host)
		return host == "box"
	})
}

func TestAgentCannotTouchAnotherAgentsRuns(t *testing.T) {
	h := newHarness(t)
	h.login()
	mk := func(name string) (id, key string) {
		_, b := h.do("POST", "/api/agents", map[string]string{"name": name}, "")
		var m map[string]string
		_ = json.Unmarshal(b, &m)
		return m["id"], m["api_key"]
	}
	idA, keyA := mk("a")
	_, keyB := mk("b")
	_, b := h.do("POST", "/api/credentials", map[string]string{"name": "w", "access_key": "AKIA12345678", "secret_key": "TOPSECRET-123456", "region": "us-east-1", "bucket": "bkt-1"}, "")
	var cred map[string]any
	_ = json.Unmarshal(b, &cred)
	code, b := h.do("POST", "/api/jobs", map[string]any{"agent_id": idA, "credential_id": cred["id"], "name": "j", "enabled": true,
		"paths": []proto.PathConfig{{Path: "/data/x", Mode: "copy"}}, "schedules": []any{}}, "")
	if code != 201 {
		t.Fatalf("job: %d %s", code, b)
	}
	var job map[string]any
	_ = json.Unmarshal(b, &job)

	// B cannot start a run for A's job, and A's config (with credentials) isn't visible to B.
	if code, _ := h.do("POST", "/api/agent/runs", proto.StartRunRequest{JobID: job["id"].(string), Trigger: "manual"}, keyB); code != 404 {
		t.Errorf("agent B started agent A's job: %d", code)
	}
	_, cfgB := h.do("GET", "/api/agent/config", nil, keyB)
	if bytes.Contains(cfgB, []byte("TOPSECRET")) {
		t.Error("agent B received agent A's credentials")
	}
	// A gets its decrypted credentials and can run + log + finish.
	_, cfgA := h.do("GET", "/api/agent/config", nil, keyA)
	if !bytes.Contains(cfgA, []byte("TOPSECRET-123456")) || !bytes.Contains(cfgA, []byte("s3.wasabisys.com")) {
		t.Errorf("agent A config missing credentials/endpoint: %s", cfgA)
	}
	code, b = h.do("POST", "/api/agent/runs", proto.StartRunRequest{JobID: job["id"].(string), Trigger: "manual"}, keyA)
	var run proto.StartRunResponse
	if code != 201 || json.Unmarshal(b, &run) != nil {
		t.Fatalf("start run: %d %s", code, b)
	}
	batch := proto.LogBatch{Lines: []proto.LogLine{{Seq: 1, TS: 1, Stream: "stdout", Line: "hello"}, {Seq: 2, TS: 2, Stream: "stderr", Line: "world"}}}
	for i := 0; i < 2; i++ { // second post is a retry: must be idempotent
		if code, b := h.do("POST", "/api/agent/runs/"+run.RunID+"/logs", batch, keyA); code != 200 {
			t.Fatalf("logs: %d %s", code, b)
		}
	}
	if code, _ := h.do("POST", "/api/agent/runs/"+run.RunID+"/logs", batch, keyB); code != 404 {
		t.Errorf("agent B wrote logs to agent A's run: %d", code)
	}
	if code, _ := h.do("POST", "/api/agent/runs/"+run.RunID+"/finish", proto.FinishRunRequest{Status: "success", Summary: "ok"}, keyA); code != 200 {
		t.Fatal("finish failed")
	}
	_, b = h.do("GET", "/api/runs/"+run.RunID+"/logs?after=0", nil, "")
	var logs struct {
		Status string
		Lines  []proto.LogLine
	}
	_ = json.Unmarshal(b, &logs)
	if logs.Status != "success" || len(logs.Lines) != 2 {
		t.Errorf("logs after retry: %+v (want 2 lines, no duplicates)", logs)
	}
}

func TestReapStaleRuns(t *testing.T) {
	h := newHarness(t)
	_, _ = h.srv.createAgent("a", "wbk_"+strings.Repeat("a", 40))
	var aid string
	_ = h.srv.db.QueryRow(`SELECT id FROM agents`).Scan(&aid)
	_, _ = h.srv.db.Exec(`INSERT INTO wasabi_credentials (id,name,access_key,secret_key_enc,region,bucket,created_at) VALUES ('c','c','k','e','us-east-1','b',1)`)
	_, _ = h.srv.db.Exec(`INSERT INTO backup_jobs (id,agent_id,credential_id,name,created_at,updated_at) VALUES ('j',?, 'c','j',1,1)`, aid)
	old := time.Now().Add(-time.Hour).Unix()
	_, _ = h.srv.db.Exec(`INSERT INTO runs (id,job_id,agent_id,trigger,status,started_at,updated_at) VALUES ('stale','j',?,'schedule','running',?,?)`, aid, old, old)
	_, _ = h.srv.db.Exec(`INSERT INTO runs (id,job_id,agent_id,trigger,status,started_at,updated_at) VALUES ('fresh','j',?,'schedule','running',?,?)`, aid, now(), now())
	h.srv.reapStaleRuns(10 * time.Minute)
	var s1, s2 string
	_ = h.srv.db.QueryRow(`SELECT status FROM runs WHERE id='stale'`).Scan(&s1)
	_ = h.srv.db.QueryRow(`SELECT status FROM runs WHERE id='fresh'`).Scan(&s2)
	if s1 != "failed" || s2 != "running" {
		t.Errorf("stale=%s fresh=%s, want failed/running", s1, s2)
	}
}

func TestJobValidation(t *testing.T) {
	good := jobInput{Name: "ok", CredentialID: "c", DestPrefix: "a/b", Paths: []proto.PathConfig{{Path: "/data/x"}}}
	if err := (&good).validate(); err != nil {
		t.Fatalf("good job rejected: %v", err)
	}
	bad := map[string]jobInput{
		"traversal prefix": {Name: "n", CredentialID: "c", DestPrefix: "../x", Paths: good.Paths},
		"absolute-looking": {Name: "n", CredentialID: "c", DestPrefix: "a//b", Paths: good.Paths},
		"no paths":         {Name: "n", CredentialID: "c"},
		"relative path":    {Name: "n", CredentialID: "c", Paths: []proto.PathConfig{{Path: "data/x"}}},
		"unclean path":     {Name: "n", CredentialID: "c", Paths: []proto.PathConfig{{Path: "/data/../etc"}}},
		"root":             {Name: "n", CredentialID: "c", Paths: []proto.PathConfig{{Path: "/"}}},
		"bad mode":         {Name: "n", CredentialID: "c", Paths: []proto.PathConfig{{Path: "/d", Mode: "rm -rf"}}},
		"dup path":         {Name: "n", CredentialID: "c", Paths: []proto.PathConfig{{Path: "/d"}, {Path: "/d"}}},
		"bad cron":         {Name: "n", CredentialID: "c", Paths: good.Paths, Schedules: []scheduleView{{CronExpr: "x", Timezone: "UTC"}}},
		"bad tz":           {Name: "n", CredentialID: "c", Paths: good.Paths, Schedules: []scheduleView{{CronExpr: "* * * * *", Timezone: "Nope/Nope"}}},
	}
	for name, in := range bad {
		in := in
		if err := (&in).validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
