package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	args := strings.Join(rcloneArgs(proto.ModeBackup, true, "/data", ":s3:b/x", ":s3:b/.versions/j/t/data"), " ")
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

func TestRcloneArgsBackupTypes(t *testing.T) {
	join := func(a []string) string { return strings.Join(a, " ") }
	// Mirror sync: deletions propagate, no history.
	if got := join(rcloneArgs(proto.ModeBackup, true, "/d", ":s3:b/h/d", "")); !strings.HasPrefix(got, "sync /d :s3:b/h/d") || strings.Contains(got, "--backup-dir") {
		t.Errorf("sync dir: %s", got)
	}
	// Incremental: same mirror, but replaced/deleted files go to the version folder.
	got := join(rcloneArgs(proto.ModeBackup, true, "/d", ":s3:b/h/d", ":s3:b/h/.versions/j1/2026-10-07T020000Z/d"))
	if !strings.HasPrefix(got, "sync /d :s3:b/h/d") || !strings.Contains(got, "--backup-dir :s3:b/h/.versions/j1/2026-10-07T020000Z/d") {
		t.Errorf("incremental dir: %s", got)
	}
	if got := rcloneArgs(proto.ModeBackup, false, "/d/f.txt", ":s3:b/h/d/f.txt", ""); got[0] != "copyto" {
		t.Errorf("single file verb = %s, want copyto", got[0])
	}
	dry := join(rcloneArgs(proto.ModeDryRun, true, "/data/x", ":s3:b/x", ":s3:b/v"))
	if !strings.Contains(dry, "--dry-run") || !strings.Contains(dry, "--backup-dir") {
		t.Errorf("dry run should simulate exactly what the backup does: %s", dry)
	}
	if strings.Contains(join(rcloneArgs(proto.ModeBackup, true, "/a", ":s3:b/a", "")), "--dry-run") {
		t.Error("a real backup must not be a dry run")
	}
	v := join(rcloneArgs(proto.ModeVerify, true, "/data/x", ":s3:b/h/data/x", ":s3:b/v"))
	if !strings.HasPrefix(v, "check /data/x :s3:b/h/data/x --one-way") || strings.Contains(v, "backup-dir") {
		t.Errorf("verify dir: %s", v)
	}
	// A single file is checked through its parent, narrowed to a literal name.
	f := rcloneArgs(proto.ModeVerify, false, "/data/a[1]*.txt", ":s3:b/h/data/a[1]*.txt", "")
	if f[0] != "check" || f[1] != "/data" || f[2] != ":s3:b/h/data" || join(f[3:6]) != `--one-way --include /a\[1\]\*.txt` {
		t.Errorf("verify file: %q", f)
	}
	for _, tr := range []string{"manual", "schedule", "", "bogus"} {
		if modeFor(tr) != proto.ModeBackup {
			t.Errorf("trigger %q should be a normal backup", tr)
		}
	}
}

func TestVersionsLayoutNeverOverlapsDestinations(t *testing.T) {
	job := proto.JobConfig{ID: "job-1", DestPrefix: "backups"}
	root := versionsRoot(wasabi, job, "nas")
	if root != ":s3:my-bucket/backups/nas/.versions/job-1" {
		t.Fatalf("versions root = %s", root)
	}
	for _, src := range []string{"/home", "/data/docs", "/v"} {
		dst := remoteFor(wasabi, job, "nas", src)
		if strings.HasPrefix(root+"/", dst+"/") || strings.HasPrefix(dst+"/", root+"/") {
			t.Errorf("rclone refuses overlapping --backup-dir: dst %s vs %s", dst, root)
		}
	}
}

func TestExpiredVersions(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	dirs := []string{"2026-10-07T020000Z", "2026-09-30T020000Z", "2026-09-01T020000Z", "2026-08-01T000000Z", "not-a-version", "2026-09-07T110000Z"}
	got := expiredVersions(dirs, now, 30)
	want := "2026-08-01T000000Z 2026-09-01T020000Z 2026-09-07T110000Z"
	if strings.Join(got, " ") != want {
		t.Errorf("expired = %v, want %s (foreign folders untouched)", got, want)
	}
	if len(expiredVersions(dirs, now, 3650)) != 0 {
		t.Error("nothing is older than 10 years")
	}
}

func TestHintFor(t *testing.T) {
	for msg, want := range map[string]string{
		"api error SignatureDoesNotMatch: The request signature": "Secret key is wrong",
		"api error InvalidAccessKeyId: ...":                      "Access key not recognised",
		"api error NoSuchBucket: The specified bucket":           "Bucket does not exist (check the name and region)",
		"dial tcp: lookup s3.nope.wasabisys.com: no such host":   "Endpoint host name does not resolve",
		"something else entirely":                                "",
	} {
		if got := hintFor(msg); got != want {
			t.Errorf("hintFor(%q) = %q, want %q", msg, got, want)
		}
	}
	if got := lastMeaningfulLine("2026/10/07 10:00:00 ERROR : Attempt 1/1 failed\n2026/10/07 10:00:00 CRITICAL: Failed to lsf: boom\n"); got != "Failed to lsf: boom" {
		t.Errorf("lastMeaningfulLine = %q", got)
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

func TestRunStatsFromRcloneOutput(t *testing.T) {
	var s runStats
	for _, l := range []string{
		"2026/10/07 10:00:00 INFO  : a: Moved (server-side)",
		"2026/10/07 10:00:00 INFO  : b: Copied (new)",
		"2026/10/07 10:00:00 INFO  : a: Copied (replaced existing)",
		"2026/10/07 10:00:00 INFO  : zz: Moved (server-side)",
		"2026/10/07 10:00:00 INFO  : zz: Moved into backup dir",
		"2026/10/07 10:00:00 INFO  : old: Deleted",
		"2026/10/07 10:00:00 ERROR : x: Failed to copy: permission denied",
		"2026/10/07 10:00:00 INFO  :    512 KiB / 1.907 MiB, 26%, 1 MiB/s, ETA 1s",
		"2026/10/07 10:00:01 INFO  :    1.907 MiB / 1.907 MiB, 100%, 0 B/s, ETA -",
	} {
		s.observe(l)
	}
	s.endInvocation()
	// S3: a move into the version folder is a server-side copy plus a delete.
	for _, l := range []string{
		"2026/10/07 11:18:10 INFO  : sub dir/d1.txt: Copied (server-side copy)",
		"2026/10/07 11:18:10 INFO  : sub dir/d1.txt: Deleted",
		"2026/10/07 11:18:10 INFO  : sub dir/d1.txt: Copied (new)",
		"2026/10/07 11:18:10 INFO  : d3.txt: Copied (server-side copy)",
		"2026/10/07 11:18:10 INFO  : d3.txt: Deleted",
		"2026/10/07 11:18:10 INFO  : d3.txt: Moved into backup dir",
		"2026/10/07 11:18:10 INFO  : gone.txt: Deleted", // a real deletion
		"2026/10/07 10:00:02 INFO  :           2 B / 2 B, 100%, 0 B/s, ETA -",
	} {
		s.observe(l)
	}
	s.endInvocation()
	got := s.snapshot()
	wantBytes := int64(1999634) + 2 // 1.907 MiB + 2 B
	if got.Transferred != 3 || got.Versioned != 4 || got.Deleted != 2 || got.Errors != 1 || got.Bytes != wantBytes {
		t.Errorf("stats = %+v, want 3 transferred, 4 versioned, 2 deleted, 1 error, %d bytes", got, wantBytes)
	}
}

func TestExcludeFlags(t *testing.T) {
	ex := []string{"/var/lib/docker/overlay2", "/var/cache", "*.sock", "/var/lib/docker/volumes/*_redis_data", "/home", "/srv/a[1]"}
	flags, skip := excludeFlags("/var", ex)
	got := strings.Join(flags, " ")
	for _, want := range []string{
		"--exclude /lib/docker/overlay2 --exclude /lib/docker/overlay2/**",
		"--exclude /cache --exclude /cache/**",
		"--exclude *.sock",
		"--exclude /lib/docker/volumes/*_redis_data --exclude /lib/docker/volumes/*_redis_data/**", // globs kept
	} {
		if skip || !strings.Contains(got, want) {
			t.Errorf("excludeFlags(/var) missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "/home") {
		t.Error("excludes outside the source must not apply")
	}
	// Entries containing * ? [ { are patterns, so "/srv/a[1]" matches a1 (documented in the UI).
	if f, _ := excludeFlags("/srv", ex); !strings.Contains(strings.Join(f, " "), "--exclude /a[1] --exclude /a[1]/**") {
		t.Errorf("pattern entries keep their globs: %q", f)
	}
	if _, skip := excludeFlags("/var/cache/apt", ex); !skip {
		t.Error("a source inside an excluded path is excluded entirely")
	}
	if _, skip := excludeFlags("/var/cachex", ex); skip {
		t.Error("/var/cachex is not inside /var/cache")
	}
}

func TestExpiredDumps(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	files := []string{
		"app-2026-10-08T020000Z.sql.gz", "app-2026-09-01T020000Z.sql.gz", "app-2026-08-01T020000Z.sql.gz",
		"app-2026-10-07T020000Z.sql.gz.partial", "app-2026-10-08T110000Z.sql.gz.partial", // old partial goes, fresh one stays
		"other-2020-01-01T000000Z.sql.gz", "notes.txt",
	}
	if got := strings.Join(expiredDumps(files, "app", now, 30), " "); got != "app-2026-08-01T020000Z.sql.gz app-2026-09-01T020000Z.sql.gz app-2026-10-07T020000Z.sql.gz.partial" {
		t.Errorf("expired = %s", got)
	}
	// Never delete the newest complete dump, however old.
	old := []string{"app-2020-01-01T000000Z.sql.gz", "app-2019-01-01T000000Z.sql.gz"}
	if got := expiredDumps(old, "app", now, 7); len(got) != 1 || got[0] != "app-2019-01-01T000000Z.sql.gz" {
		t.Errorf("newest dump must survive: %v", got)
	}
}

func TestDumpPasswordNeverInArgsOrEnv(t *testing.T) {
	a := &Agent{cfg: &Config{DockerPath: "docker"}}
	t.Setenv("AGENT_API_KEY", "wbk_secret")
	d := proto.DumpConfig{Name: "db", Container: "mysql", User: "root", Password: `p"a\ss w0rd`, Databases: []string{"ninja"}}
	cmd, err := a.mysqlCommand(context.Background(), d, "dump", append(mysqlDumpFlags, dumpDBArgs(d)...))
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(cmd.Args, " ") + strings.Join(cmd.Env, " ")
	if strings.Contains(all, "w0rd") || strings.Contains(all, "wbk_secret") {
		t.Errorf("secret leaked into argv/env: %s", all)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "exec -i mysql sh -c") || !strings.HasSuffix(strings.Join(cmd.Args, " "), "--databases ninja") {
		t.Errorf("args = %q", cmd.Args)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	if string(in) != "[client]\nuser=root\npassword=\"p\\\"a\\\\ss w0rd\"\n" {
		t.Errorf("option file = %q", in)
	}
	// Container-env mode sends nothing on stdin: the password never leaves the container.
	d.UseContainerEnv, d.Password = true, ""
	cmd, _ = a.mysqlCommand(context.Background(), d, "client", []string{"-e", "SELECT 1"})
	if cmd.Stdin != nil || !strings.Contains(strings.Join(cmd.Args, " "), "MYSQL_ROOT_PASSWORD") {
		t.Errorf("env mode: stdin=%v args=%q", cmd.Stdin, cmd.Args)
	}
}
