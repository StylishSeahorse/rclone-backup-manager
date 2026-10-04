package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

var wasabi = proto.WasabiConfig{AccessKey: "AKIAEXAMPLE0001", SecretKey: "s3cr3t-value-xyz", Region: "eu-central-1", Bucket: "my-bucket", Endpoint: "s3.eu-central-1.wasabisys.com"}

func TestRemoteFor(t *testing.T) {
	job := proto.JobConfig{DestPrefix: "backups/prod"}
	got := remoteFor(wasabi, job, "nas 01/../x", "/host/home/me/docs")
	want := ":s3:my-bucket/backups/prod/nas_01_.._x/host/home/me/docs"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := remoteFor(wasabi, proto.JobConfig{}, "a", "/data"); got != ":s3:my-bucket/a/data" {
		t.Errorf("no prefix: got %q", got)
	}
	if got := remoteFor(wasabi, proto.JobConfig{DestPrefix: "p"}, "..", "/data"); got != ":s3:my-bucket/p/_/data" {
		t.Errorf("agent name \"..\" must not become a traversal segment: got %q", got)
	}
}

// The core security property: credentials reach rclone via environment only.
func TestCredentialsOnlyInEnvironment(t *testing.T) {
	t.Setenv("AGENT_API_KEY", "wbk_must_not_leak")
	t.Setenv("SOME_OTHER_SECRET", "nope")
	env := strings.Join(rcloneEnv(wasabi), "\n")
	for _, must := range []string{"AWS_ACCESS_KEY_ID=AKIAEXAMPLE0001", "AWS_SECRET_ACCESS_KEY=s3cr3t-value-xyz",
		"RCLONE_S3_PROVIDER=Wasabi", "RCLONE_S3_ENV_AUTH=true", "RCLONE_S3_REGION=eu-central-1", "RCLONE_CONFIG=/dev/null"} {
		if !strings.Contains(env, must) {
			t.Errorf("env missing %q", must)
		}
	}
	for _, mustNot := range []string{"wbk_must_not_leak", "SOME_OTHER_SECRET", "AGENT_"} {
		if strings.Contains(env, mustNot) {
			t.Errorf("agent environment leaked into rclone: %q", mustNot)
		}
	}
	args := strings.Join(rcloneArgs("copy", true, "/data", ":s3:b/x"), " ")
	if strings.Contains(args, wasabi.SecretKey) || strings.Contains(args, wasabi.AccessKey) {
		t.Error("credentials must never appear on the command line")
	}
}

func TestEndpointSelectsProvider(t *testing.T) {
	cases := map[string]struct{ provider, pathStyle string }{
		"s3.wasabisys.com":                   {"Wasabi", "false"},
		"s3.eu-central-1.wasabisys.com":      {"Wasabi", "false"},
		"https://s3.us-west-1.wasabisys.com": {"Wasabi", "false"},
		"http://minio.internal:9000":         {"Other", "true"},
		"s3.wasabisys.com.evil.example":      {"Other", "true"}, // suffix tricks must not select Wasabi
		"evilwasabisys.com":                  {"Other", "true"},
	}
	for ep, want := range cases {
		w := wasabi
		w.Endpoint = ep
		env := strings.Join(rcloneEnv(w), "\n")
		if !strings.Contains(env, "RCLONE_S3_PROVIDER="+want.provider+"\n") || !strings.Contains(env, "RCLONE_S3_FORCE_PATH_STYLE="+want.pathStyle+"\n") {
			t.Errorf("endpoint %q: want provider=%s path-style=%s, env:\n%s", ep, want.provider, want.pathStyle, env)
		}
	}
}

func TestRcloneArgsVerb(t *testing.T) {
	cases := []struct {
		mode  string
		isDir bool
		want  string
	}{{"copy", true, "copy"}, {"sync", true, "sync"}, {"sync", false, "copyto"}, {"copy", false, "copyto"}}
	for _, c := range cases {
		if got := rcloneArgs(c.mode, c.isDir, "/s", ":s3:b/d")[0]; got != c.want {
			t.Errorf("mode=%s isDir=%v: verb %s, want %s", c.mode, c.isDir, got, c.want)
		}
	}
}

func TestSplitLinesHandlesCarriageReturns(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("one\ntwo\rthree\r\nfour"))
	sc.Split(splitLines)
	var got []string
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			got = append(got, s)
		}
	}
	if strings.Join(got, "|") != "one|two|three|four" {
		t.Errorf("got %v", got)
	}
}

// fakeDashboard records the log batches the agent posts.
type fakeDashboard struct {
	mu    sync.Mutex
	lines []proto.LogLine
	fails int // first N requests answered with 503
}

func (f *fakeDashboard) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		http.Error(w, `{"error":"down"}`, http.StatusServiceUnavailable)
		return
	}
	var b proto.LogBatch
	_ = json.NewDecoder(r.Body).Decode(&b)
	f.lines = append(f.lines, b.Lines...)
	_, _ = w.Write([]byte(`{}`))
}

func newTestLogger(t *testing.T, f *fakeDashboard) *runLogger {
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	api := &apiClient{base: srv.URL, key: "wbk_x", http: srv.Client()}
	return newRunLogger(api, "run-1", wasabi.SecretKey, wasabi.AccessKey)
}

func TestRunLoggerRedactsAndOrders(t *testing.T) {
	f := &fakeDashboard{}
	lg := newTestLogger(t, f)
	lg.start()
	lg.add("stdout", "plain line")
	lg.add("stderr", "oops key="+wasabi.SecretKey+" id="+wasabi.AccessKey)
	lg.agent("done")
	lg.close(context.Background())

	if len(f.lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(f.lines))
	}
	for i, l := range f.lines {
		if l.Seq != int64(i+1) {
			t.Errorf("line %d has seq %d", i, l.Seq)
		}
		if strings.Contains(l.Line, wasabi.SecretKey) || strings.Contains(l.Line, wasabi.AccessKey) {
			t.Errorf("secret reached the dashboard: %q", l.Line)
		}
	}
	if f.lines[1].Line != "oops key=*** id=***" || f.lines[1].Stream != "stderr" {
		t.Errorf("line 2 = %+v", f.lines[1])
	}
}

func TestRunLoggerKeepsLinesWhenDashboardIsDown(t *testing.T) {
	f := &fakeDashboard{fails: 1}
	lg := newTestLogger(t, f)
	lg.add("stdout", "a")
	lg.add("stdout", "b")
	lg.flush(context.Background(), 1) // dashboard answers 503: lines must be retained
	if len(f.lines) != 0 {
		t.Fatal("nothing should have been delivered yet")
	}
	lg.add("stdout", "c")
	lg.flush(context.Background(), 1)
	var seqs []int64
	for _, l := range f.lines {
		seqs = append(seqs, l.Seq)
	}
	if len(seqs) != 3 || seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 {
		t.Errorf("after recovery got seqs %v, want [1 2 3] in order", seqs)
	}
}

func TestSchedulerAppliesOnlyEnabledValidSchedules(t *testing.T) {
	s := NewScheduler(func(string) {})
	s.Apply(&proto.AgentConfig{Jobs: []proto.JobConfig{
		{ID: "a", Name: "a", Enabled: true, Schedules: []proto.ScheduleSpec{{Cron: "0 2 * * *", Timezone: "Europe/Berlin"}, {Cron: "garbage", Timezone: "UTC"}}},
		{ID: "b", Name: "b", Enabled: false, Schedules: []proto.ScheduleSpec{{Cron: "@every 1h", Timezone: "UTC"}}},
	}})
	defer s.Stop()
	if n := len(s.cron.Entries()); n != 1 {
		t.Errorf("active entries = %d, want 1 (disabled job and invalid spec skipped)", n)
	}
}
