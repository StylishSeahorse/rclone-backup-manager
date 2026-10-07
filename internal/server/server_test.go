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
			reply := handle(env)
			if env.ID == "" { // notification: no reply expected
				continue
			}
			out, _ := json.Marshal(reply)
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

func TestSchedulePreviewAndNextRun(t *testing.T) {
	h := newHarness(t)
	h.login()
	code, b := h.do("POST", "/api/schedules/preview", map[string]string{"cron_expr": "30 2 * * 1,3", "timezone": "Europe/Berlin"}, "")
	var res struct{ Next []int64 }
	if code != 200 || json.Unmarshal(b, &res) != nil || len(res.Next) != 5 {
		t.Fatalf("preview: %d %s", code, b)
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	for i, ts := range res.Next {
		lt := time.Unix(ts, 0).In(berlin)
		if lt.Hour() != 2 || lt.Minute() != 30 || (lt.Weekday() != time.Monday && lt.Weekday() != time.Wednesday) {
			t.Errorf("run %d at %v is not Mon/Wed 02:30 Berlin time", i, lt)
		}
		if i > 0 && ts <= res.Next[i-1] {
			t.Error("preview times must increase")
		}
	}
	for _, bad := range []map[string]string{{"cron_expr": "nope", "timezone": "UTC"}, {"cron_expr": "0 2 * * *", "timezone": "Mars/Base"}} {
		if code, _ := h.do("POST", "/api/schedules/preview", bad, ""); code != 400 {
			t.Errorf("preview %v = %d, want 400", bad, code)
		}
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := nextRun([]scheduleView{
		{CronExpr: "0 5 * * *", Timezone: "UTC", Enabled: true},
		{CronExpr: "0 1 * * *", Timezone: "UTC", Enabled: false}, // disabled: ignored
		{CronExpr: "0 3 * * *", Timezone: "UTC", Enabled: true},
	}, now)
	if got == nil || time.Unix(*got, 0).UTC().Hour() != 3 {
		t.Errorf("nextRun = %v, want 03:00", got)
	}
	if nextRun(nil, now) != nil {
		t.Error("no schedules => no next run")
	}
}

func TestCredentialTestAndRunModesGoThroughAgent(t *testing.T) {
	h := newHarness(t)
	h.login()
	// No agent online yet.
	if code, _ := h.do("POST", "/api/credentials/test", map[string]any{"credential": map[string]string{"access_key": "AKIA12345678", "secret_key": "TOPSECRET-123456", "region": "us-east-1", "bucket": "bkt-1"}}, ""); code != 503 {
		t.Errorf("test with no agent online = %d, want 503", code)
	}
	_, b := h.do("POST", "/api/agents", map[string]string{"name": "box"}, "")
	var ag map[string]string
	_ = json.Unmarshal(b, &ag)
	_, b = h.do("POST", "/api/credentials", map[string]string{"name": "w", "access_key": "AKIA12345678", "secret_key": "TOPSECRET-123456", "region": "eu-central-1", "bucket": "bkt-1"}, "")
	var cred map[string]any
	_ = json.Unmarshal(b, &cred)

	got := make(chan proto.Envelope, 10)
	h.fakeAgent(ag["api_key"], func(env proto.Envelope) proto.Envelope {
		got <- env
		switch env.Type {
		case proto.MsgTestCreds:
			res, _ := json.Marshal(proto.CredentialTestResult{OK: true, Steps: []proto.TestStep{{Name: "Connect and list bucket", Status: "ok"}}})
			return proto.Envelope{ID: env.ID, Type: proto.MsgResult, Payload: res}
		case proto.MsgStatus:
			res, _ := json.Marshal(proto.AgentStatus{Hostname: "box", RunningJobs: []string{}})
			return proto.Envelope{ID: env.ID, Type: proto.MsgResult, Payload: res}
		}
		return proto.Envelope{}
	})
	waitFor(t, func() bool { return h.srv.hub.Online(ag["id"]) })
	drain := func(typ string) proto.Envelope {
		t.Helper()
		for {
			select {
			case e := <-got:
				if e.Type == typ {
					return e
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("agent never received %s", typ)
			}
		}
	}

	// Editing a saved credential: blank secret means "use the stored one".
	code, b := h.do("POST", "/api/credentials/test", map[string]any{"credential_id": cred["id"], "write_test": true,
		"credential": map[string]string{"access_key": "AKIA12345678", "region": "eu-central-1", "bucket": "bkt-1"}}, "")
	if code != 200 || !bytes.Contains(b, []byte(`"ok":true`)) || bytes.Contains(b, []byte("TOPSECRET")) {
		t.Fatalf("credential test: %d %s", code, b)
	}
	var req proto.CredentialTestRequest
	_ = json.Unmarshal(drain(proto.MsgTestCreds).Payload, &req)
	if req.Wasabi.SecretKey != "TOPSECRET-123456" || req.Wasabi.Endpoint != "s3.eu-central-1.wasabisys.com" || !req.WriteTest {
		t.Errorf("agent got %+v", req)
	}
	// Unsaved credential without a secret cannot be tested.
	if code, _ := h.do("POST", "/api/credentials/test", map[string]any{"credential": map[string]string{"access_key": "AKIA12345678", "region": "us-east-1", "bucket": "bkt-1"}}, ""); code != 400 {
		t.Errorf("secretless test = %d, want 400", code)
	}

	// Agent connection test.
	code, b = h.do("GET", "/api/agents/"+ag["id"]+"/status", nil, "")
	if code != 200 || !bytes.Contains(b, []byte(`"rtt_ms"`)) || !bytes.Contains(b, []byte(`"hostname":"box"`)) {
		t.Errorf("status: %d %s", code, b)
	}

	// Run modes reach the agent; unknown modes are refused.
	code, b = h.do("POST", "/api/jobs", map[string]any{"agent_id": ag["id"], "credential_id": cred["id"], "name": "j", "enabled": true,
		"paths": []proto.PathConfig{{Path: "/data/x", Mode: "copy"}}, "schedules": []any{}}, "")
	var job map[string]any
	_ = json.Unmarshal(b, &job)
	if code, _ := h.do("POST", "/api/jobs/"+job["id"].(string)+"/run", map[string]string{"mode": "rm-rf"}, ""); code != 400 {
		t.Errorf("bogus mode = %d, want 400", code)
	}
	if code, _ := h.do("POST", "/api/jobs/"+job["id"].(string)+"/run", map[string]string{"mode": "verify"}, ""); code != 202 {
		t.Errorf("verify run = %d", code)
	}
	var rn proto.RunNowRequest
	_ = json.Unmarshal(drain(proto.MsgRunNow).Payload, &rn)
	if rn.Mode != proto.ModeVerify {
		t.Errorf("agent got mode %q, want verify", rn.Mode)
	}
	if code, _ := h.do("POST", "/api/agent/runs", proto.StartRunRequest{JobID: job["id"].(string), Trigger: "dry-run"}, ag["api_key"]); code != 201 {
		t.Errorf("agent could not register a dry-run: %d", code)
	}
	if code, _ := h.do("POST", "/api/agent/runs", proto.StartRunRequest{JobID: job["id"].(string), Trigger: "evil"}, ag["api_key"]); code != 400 {
		t.Errorf("unknown trigger accepted: %d", code)
	}
}

func TestMigrateOldDatabaseKeepsSyncJobsAsSync(t *testing.T) {
	dir := t.TempDir()
	old, err := openRaw(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The pre-backup_type schema, with one per-path "sync" job and one "copy" job.
	for _, q := range []string{
		`CREATE TABLE agents (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, api_key_hash TEXT NOT NULL UNIQUE, hostname TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '', os TEXT NOT NULL DEFAULT '', rclone_ver TEXT NOT NULL DEFAULT '', browse_roots TEXT NOT NULL DEFAULT '[]', last_seen_at BIGINT NOT NULL DEFAULT 0, created_at BIGINT NOT NULL)`,
		`CREATE TABLE wasabi_credentials (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, access_key TEXT NOT NULL, secret_key_enc TEXT NOT NULL, region TEXT NOT NULL, bucket TEXT NOT NULL, endpoint TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL)`,
		`CREATE TABLE backup_jobs (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, credential_id TEXT NOT NULL, name TEXT NOT NULL, dest_prefix TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL)`,
		`CREATE TABLE backup_paths (id TEXT NOT NULL PRIMARY KEY, job_id TEXT NOT NULL, path TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'copy')`,
		`INSERT INTO backup_jobs VALUES ('mirror','a','c','m','',1,1,1), ('plain','a','c','p','',1,1,1)`,
		`INSERT INTO backup_paths VALUES ('p1','mirror','/x','sync'), ('p2','plain','/y','copy')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	db, err := openDB(dir)
	if err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}
	defer db.Close()
	for id, want := range map[string]string{"mirror": "sync", "plain": "incremental"} {
		var bt string
		var keep int
		if err := db.QueryRow(`SELECT backup_type, retention_days FROM backup_jobs WHERE id = ?`, id).Scan(&bt, &keep); err != nil || bt != want || keep != 30 {
			t.Errorf("job %s: type=%q keep=%d err=%v, want %s/30", id, bt, keep, err, want)
		}
	}
	db.Close()
	if db2, err := openDB(dir); err != nil { // migrating twice must be a no-op
		t.Errorf("second open: %v", err)
	} else {
		db2.Close()
	}
}

func TestJobBackupTypeValidation(t *testing.T) {
	base := func() jobInput {
		return jobInput{Name: "n", CredentialID: "c", Paths: []proto.PathConfig{{Path: "/data/x"}}}
	}
	in := base()
	if err := (&in).validate(); err != nil || in.BackupType != "incremental" || in.RetentionDays == nil || *in.RetentionDays != 30 {
		t.Errorf("defaults: %v %q %v", err, in.BackupType, in.RetentionDays)
	}
	zero := 0
	in = base()
	in.BackupType, in.RetentionDays = "sync", &zero
	if err := (&in).validate(); err != nil {
		t.Errorf("sync with retention 0 rejected: %v", err)
	}
	neg := -1
	for name, mut := range map[string]func(*jobInput){
		"bad type":         func(j *jobInput) { j.BackupType = "copy-everything" },
		"negative keep":    func(j *jobInput) { j.RetentionDays = &neg },
		"versions is ours": func(j *jobInput) { j.Paths = []proto.PathConfig{{Path: "/.versions/x"}} },
	} {
		in := base()
		mut(&in)
		if err := (&in).validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
