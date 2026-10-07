package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// ---- Building the rclone invocation --------------------------------------------

var unsafeSeg = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// agentRoot is where everything from this agent lives:
//
//	:s3:<bucket>/<job prefix>/<agent name>
//
// ":s3:" is an on-the-fly rclone backend, so no rclone.conf is ever written;
// everything else comes from environment variables (see rcloneEnv).
func agentRoot(w proto.WasabiConfig, job proto.JobConfig, agentName string) string {
	parts := []string{w.Bucket}
	if job.DestPrefix != "" {
		parts = append(parts, job.DestPrefix)
	}
	seg := unsafeSeg.ReplaceAllString(agentName, "_")
	if seg == "" || seg == "." || seg == ".." { // never let the name act as a path traversal
		seg = "_"
	}
	return ":s3:" + strings.Join(append(parts, seg), "/")
}

// remoteFor returns the destination for a source path: <agentRoot>/<absolute path>.
func remoteFor(w proto.WasabiConfig, job proto.JobConfig, agentName, src string) string {
	return agentRoot(w, job, agentName) + "/" + strings.TrimPrefix(path.Clean(src), "/")
}

// versionsRoot holds an incremental job's history:
// <agentRoot>/.versions/<job id>/<run timestamp>/<absolute path>. It is a
// sibling of every backed-up path, so it never overlaps a sync destination.
func versionsRoot(w proto.WasabiConfig, job proto.JobConfig, agentName string) string {
	return agentRoot(w, job, agentName) + "/.versions/" + unsafeSeg.ReplaceAllString(job.ID, "_")
}

// versionStamp names a run's version folder; it sorts and parses as time.
const versionStamp = "2006-01-02T150405Z"

// rcloneEnv builds the child's *entire* environment. Credentials live only in
// this process's memory and the child's environment: never in argv (visible in
// `ps`), never in rclone.conf, never on disk. The agent's own environment
// (including its dashboard API key) is deliberately not inherited.
func rcloneEnv(w proto.WasabiConfig) []string {
	// rclone's Wasabi provider hard-wires virtual-hosted-style addressing
	// (bucket.host), which only resolves on real Wasabi. A custom endpoint
	// (MinIO, a test server, ...) needs the generic provider with path-style.
	provider, pathStyle := "Wasabi", "false"
	if !isWasabiEndpoint(w.Endpoint) {
		provider, pathStyle = "Other", "true"
	}
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"RCLONE_CONFIG=/dev/null", // never read or write a config file

		// Standard AWS variables, picked up because env_auth=true.
		"AWS_ACCESS_KEY_ID=" + w.AccessKey,
		"AWS_SECRET_ACCESS_KEY=" + w.SecretKey,

		"RCLONE_S3_PROVIDER=" + provider,
		"RCLONE_S3_FORCE_PATH_STYLE=" + pathStyle,
		"RCLONE_S3_ENV_AUTH=true",
		"RCLONE_S3_REGION=" + w.Region,
		"RCLONE_S3_ENDPOINT=" + w.Endpoint,
		"RCLONE_S3_NO_CHECK_BUCKET=true", // keys limited to object access can't CreateBucket
	}
}

// isWasabiEndpoint reports whether endpoint (host or URL) belongs to wasabisys.com.
func isWasabiEndpoint(endpoint string) bool {
	host := endpoint
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	return host == "wasabisys.com" || strings.HasSuffix(host, ".wasabisys.com")
}

// rcloneArgs builds the command for one source path. runMode is the run's
// mode (backup, dry-run, verify). backupDir, when set (incremental jobs),
// receives every file the run would otherwise overwrite or delete.
func rcloneArgs(runMode string, isDir bool, src, dst, backupDir string) []string {
	common := []string{
		"--log-level", "INFO",
		"--stats", "10s", "--stats-one-line",
		"--checkers", "8",
		"--retries", "3", "--low-level-retries", "10",
	}
	if runMode == proto.ModeVerify {
		// --one-way: every source file must exist, with matching size and
		// hash, at the destination; extra remote files are fine.
		if isDir {
			return append([]string{"check", src, dst, "--one-way"}, common...)
		}
		// check works on directories, so compare the parent narrowed to this file.
		return append([]string{"check", path.Dir(src), path.Dir(dst), "--one-way",
			"--include", "/" + escapeGlob(path.Base(src))}, common...)
	}
	// Both backup types mirror the source (new files added, deleted files
	// removed); incremental differs only by keeping what is replaced/removed.
	verb := "sync"
	if !isDir {
		verb = "copyto" // single file -> single object
	}
	args := append([]string{verb, src, dst, "--transfers", "4"}, common...)
	if backupDir != "" {
		args = append(args, "--backup-dir", backupDir)
	}
	if runMode == proto.ModeDryRun {
		args = append(args, "--dry-run")
	}
	return args
}

// escapeGlob makes a file name literal in an rclone filter pattern.
func escapeGlob(name string) string {
	var b strings.Builder
	for _, r := range name {
		if strings.ContainsRune(`\*?[]{}`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// modeFor maps a run trigger to its run mode.
func modeFor(trigger string) string {
	switch trigger {
	case proto.ModeDryRun, proto.ModeVerify:
		return trigger
	}
	return proto.ModeBackup
}

// rcloneVersion returns the first line of `rclone version`, e.g. "rclone v1.68.2".
func rcloneVersion(bin string) (string, error) {
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return "", fmt.Errorf("cannot execute rclone (%s): %w", bin, err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line), nil
}

// ---- Running a job ---------------------------------------------------------------

type activeRun struct {
	runID  string
	cancel context.CancelFunc
	user   bool // cancelled by an operator (vs. agent shutdown)
}

// runJob executes one job end to end and reports it to the dashboard.
// A job never runs twice concurrently: a second trigger is skipped.
func (a *Agent) runJob(parent context.Context, jobID, trigger string) {
	job := a.jobByID(jobID)
	if job == nil {
		log.Printf("run requested for unknown job %s", jobID)
		return
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	ar := &activeRun{cancel: cancel}
	a.mu.Lock()
	if _, busy := a.running[jobID]; busy {
		a.mu.Unlock()
		log.Printf("job %q is already running; skipping %s trigger", job.Name, trigger)
		return
	}
	a.running[jobID] = ar
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.running, jobID)
		a.mu.Unlock()
	}()

	runID, err := a.api.StartRun(ctx, jobID, trigger)
	if err != nil {
		log.Printf("job %q: cannot register run with dashboard: %v", job.Name, err)
		return
	}
	a.mu.Lock()
	ar.runID = runID
	a.mu.Unlock()

	lg := newRunLogger(a.api, runID, job.Wasabi.SecretKey, job.Wasabi.AccessKey)
	lg.start()

	status, code, summary := a.execute(ctx, ar, job, modeFor(trigger), lg)

	// The run is over even if ctx was cancelled, so use a fresh context to
	// flush the remaining log lines and the final status.
	fctx, fcancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer fcancel()
	lg.close(fctx)
	if err := a.api.FinishRun(fctx, runID, proto.FinishRunRequest{Status: status, ExitCode: code, Summary: summary}); err != nil {
		log.Printf("job %q: could not report final status: %v", job.Name, err)
	}
	log.Printf("job %q finished: %s (%s)", job.Name, status, summary)
}

func (a *Agent) execute(ctx context.Context, ar *activeRun, job *proto.JobConfig, mode string, lg *runLogger) (status string, code int, summary string) {
	verb := map[string]string{proto.ModeBackup: "backed up", proto.ModeDryRun: "simulated", proto.ModeVerify: "verified"}[mode]
	switch mode {
	case proto.ModeDryRun:
		lg.agent("DRY RUN of %q: nothing will be uploaded or deleted; rclone lists what it would do", job.Name)
	case proto.ModeVerify:
		lg.agent("VERIFY %q: checking every source file exists unchanged in wasabi://%s/%s", job.Name, job.Wasabi.Bucket, job.DestPrefix)
	default:
		lg.agent("starting %q: %d path(s) -> wasabi://%s/%s", job.Name, len(job.Paths), job.Wasabi.Bucket, job.DestPrefix)
	}
	incremental := job.BackupType != proto.BackupSync
	stamp := time.Now().UTC().Format(versionStamp)
	if mode != proto.ModeVerify {
		if incremental {
			keep := "forever"
			if job.Retention > 0 {
				keep = fmt.Sprintf("for %d days", job.Retention)
			}
			lg.agent("incremental: changed and deleted files are kept under .versions/%s/%s (kept %s)", job.ID, stamp, keep)
		} else {
			lg.agent("mirror sync: files deleted locally will be deleted from Wasabi, no history is kept")
		}
	}

	// Bound concurrency across jobs on this agent.
	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	case <-ctx.Done():
		return proto.StatusCancelled, 1, "cancelled while queued"
	}

	ok, failed := 0, 0
	lastCode := 0
	for _, p := range job.Paths {
		if ctx.Err() != nil {
			break
		}
		// Re-validate on every run: the dashboard is not trusted to keep the
		// agent inside its mounted roots.
		src, err := a.scanner.Resolve(p.Path)
		if err != nil {
			lg.agent("SKIP %s: %v", p.Path, err)
			failed++
			lastCode = 1
			continue
		}
		fi, err := os.Stat(src)
		if err != nil {
			lg.agent("SKIP %s: %v", p.Path, err)
			failed++
			lastCode = 1
			continue
		}
		dst := remoteFor(job.Wasabi, *job, a.name(), src)
		backupDir := ""
		if incremental {
			backupDir = versionsRoot(job.Wasabi, *job, a.name()) + "/" + stamp + "/" + strings.TrimPrefix(path.Clean(src), "/")
		}
		args := rcloneArgs(mode, fi.IsDir(), src, dst, backupDir)
		// The command line contains no secrets, so it is safe to show.
		lg.agent("$ rclone %s", strings.Join(args, " "))

		c, err := a.runRclone(ctx, args, rcloneEnv(job.Wasabi), lg)
		switch {
		case ctx.Err() != nil:
		case err != nil:
			lg.agent("FAILED %s: %v", p.Path, err)
			failed++
			lastCode = c
		default:
			lg.agent("OK %s", p.Path)
			ok++
		}
	}

	if ctx.Err() != nil {
		a.mu.RLock()
		byUser := ar.user
		a.mu.RUnlock()
		msg := "cancelled"
		if !byUser {
			msg = "cancelled: agent shutting down"
		}
		lg.agent("%s", msg)
		return proto.StatusCancelled, 130, msg
	}
	if mode == proto.ModeBackup && incremental && job.Retention > 0 && failed == 0 {
		a.pruneVersions(ctx, job, lg)
	}
	summary = fmt.Sprintf("%d of %d path(s) %s", ok, len(job.Paths), verb)
	if failed > 0 {
		if lastCode == 0 {
			lastCode = 1
		}
		return proto.StatusFailed, lastCode, summary + fmt.Sprintf(", %d failed", failed)
	}
	return proto.StatusSuccess, 0, summary
}

// runRclone executes rclone and streams stdout and stderr, line by line, to lg
// as they are produced. It returns the process exit code.
func (a *Agent) runRclone(ctx context.Context, args, env []string, lg *runLogger) (int, error) {
	cmd := exec.CommandContext(ctx, a.cfg.RclonePath, args...)
	cmd.Env = env
	// On cancel, ask rclone to stop cleanly (it finishes in-flight uploads'
	// bookkeeping) and only SIGKILL if it hasn't exited after WaitDelay.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	cmd.WaitDelay = 15 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 1, err
	}
	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start rclone: %w", err)
	}

	var wg sync.WaitGroup
	pump := func(r io.Reader, stream string) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sc.Split(splitLines)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				lg.add(stream, t)
			}
		}
		// A pathological >1MiB line makes Scanner stop; drain so rclone never
		// blocks on a full pipe.
		_, _ = io.Copy(io.Discard, r)
	}
	wg.Add(2)
	go pump(stdout, "stdout")
	go pump(stderr, "stderr")
	wg.Wait() // must finish reading before Wait closes the pipes

	err = cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), fmt.Errorf("rclone exited with code %d", ee.ExitCode())
	}
	return 1, err
}

// splitLines splits on \n or \r, so rclone's \r-refreshed progress output
// becomes separate lines instead of one ever-growing token.
func splitLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// ---- Real-time log streaming -------------------------------------------------------

const (
	flushInterval = time.Second
	flushLines    = 200
	maxBuffered   = 20000  // lines held while the dashboard is unreachable
	maxRunLines   = 500000 // matches the server's per-run cap
)

// runLogger batches log lines and posts them to the dashboard about once a
// second, so the UI can tail a run live. Lines carry a sequence number, which
// makes retries idempotent on the server.
type runLogger struct {
	api    *apiClient
	runID  string
	redact []string

	mu      sync.Mutex
	seq     int64
	buf     []proto.LogLine
	dropped bool

	kick chan struct{}
	stop chan struct{}
	done chan struct{}
}

func newRunLogger(api *apiClient, runID string, secrets ...string) *runLogger {
	var rs []string
	for _, s := range secrets {
		if len(s) >= 4 {
			rs = append(rs, s)
		}
	}
	return &runLogger{api: api, runID: runID, redact: rs,
		kick: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
}

func (l *runLogger) start() { go l.loop() }

func (l *runLogger) agent(format string, a ...any) { l.add("agent", fmt.Sprintf(format, a...)) }

func (l *runLogger) add(stream, line string) {
	// Defence in depth: rclone doesn't print secrets, but if anything ever
	// echoed one it must not reach the dashboard's history.
	for _, s := range l.redact {
		line = strings.ReplaceAll(line, s, "***")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seq >= maxRunLines {
		if !l.dropped {
			l.dropped = true
			l.seq++
			l.buf = append(l.buf, proto.LogLine{Seq: l.seq, TS: time.Now().UnixMilli(), Stream: "agent", Line: "log limit reached; further output is not recorded"})
		}
		return
	}
	l.seq++
	l.buf = append(l.buf, proto.LogLine{Seq: l.seq, TS: time.Now().UnixMilli(), Stream: stream, Line: line})
	if len(l.buf) >= flushLines {
		select {
		case l.kick <- struct{}{}:
		default:
		}
	}
}

func (l *runLogger) loop() {
	defer close(l.done)
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-l.kick:
		case <-l.stop:
			return
		}
		l.flush(context.Background(), 1)
	}
}

// flush posts everything buffered. On failure the lines go back to the front of
// the buffer (bounded by maxBuffered) and are retried on the next tick.
func (l *runLogger) flush(ctx context.Context, attempts int) {
	for {
		l.mu.Lock()
		n := len(l.buf)
		if n > 2000 {
			n = 2000
		}
		batch := append([]proto.LogLine(nil), l.buf[:n]...)
		l.buf = l.buf[n:]
		l.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := withRetry(rctx, attempts, func() error { return l.api.PostLogs(rctx, l.runID, batch) })
		cancel()
		if err != nil {
			l.mu.Lock()
			l.buf = append(batch, l.buf...)
			if len(l.buf) > maxBuffered { // drop the oldest rather than grow without bound
				l.buf = l.buf[len(l.buf)-maxBuffered:]
			}
			l.mu.Unlock()
			return
		}
	}
}

// close stops the background flusher and does a final, retrying flush.
func (l *runLogger) close(ctx context.Context) {
	close(l.stop)
	<-l.done
	l.flush(ctx, 5)
}

// pruneVersions deletes version folders older than the job's retention. Age is
// taken from the folder's run timestamp, not file times: moved files keep
// their original modification time, which says nothing about when they were
// replaced.
func (a *Agent) pruneVersions(ctx context.Context, job *proto.JobConfig, lg *runLogger) {
	root := versionsRoot(job.Wasabi, *job, a.name())
	env := rcloneEnv(job.Wasabi)
	var dirs []string
	_, err := a.rcloneOnce(ctx, env, nil, func(l string) { dirs = append(dirs, strings.TrimSuffix(l, "/")) },
		"lsf", root, "--dirs-only", "--max-depth", "1")
	if err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "not found") { // no versions yet is normal
			lg.agent("retention: could not list versions: %v", err)
		}
		return
	}
	for _, d := range expiredVersions(dirs, time.Now(), job.Retention) {
		lg.agent("retention: removing versions from %s (older than %d days)", d, job.Retention)
		if _, err := a.runRclone(ctx, []string{"purge", root + "/" + d, "--log-level", "NOTICE"}, env, lg); err != nil {
			lg.agent("retention: failed to remove %s: %v", d, err)
		}
	}
}

// expiredVersions returns the version folder names older than days.
// Folders that don't parse as run timestamps are never touched.
func expiredVersions(dirs []string, now time.Time, days int) []string {
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	var out []string
	for _, d := range dirs {
		t, err := time.Parse(versionStamp, d)
		if err == nil && t.Before(cutoff) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
