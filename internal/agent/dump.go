package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// MySQL/MariaDB dumps.
//
// A live database's files copied from disk are usually not restorable, so
// databases are dumped with mysqldump (--single-transaction: a consistent
// snapshot without locking InnoDB tables) and the SQL is gzipped and streamed
// straight to Wasabi with `rclone rcat` - nothing is written to local disk.
//
// The password never appears on a command line or in an environment variable:
// it is handed to mysqldump as an option file on stdin, or, for Docker
// containers, read from the container's own MYSQL_ROOT_PASSWORD inside it.

const dumpCompleteMarker = "-- Dump completed"

// mysqlFlags are the dump options: consistent, streaming, and complete
// (stored procedures, triggers, events, binary data).
var mysqlDumpFlags = []string{"--single-transaction", "--quick", "--routines", "--triggers", "--events", "--hex-blob"}

func optionFile(user, password string) string {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(password)
	return fmt.Sprintf("[client]\nuser=%s\npassword=\"%s\"\n", user, esc)
}

func dumpDBArgs(d proto.DumpConfig) []string {
	if len(d.Databases) == 0 {
		return []string{"--all-databases"}
	}
	return append([]string{"--databases"}, d.Databases...)
}

// containerScript runs inside the database container. $1 is the user, the
// remaining arguments go to the tool. "tool" is dump or client.
func containerScript(tool string, useEnv bool) string {
	find := `D=$(command -v mysqldump || command -v mariadb-dump || true); [ -n "$D" ] || { echo "mysqldump/mariadb-dump not found in the container" >&2; exit 127; }`
	if tool == "client" {
		find = `D=$(command -v mysql || command -v mariadb || true); [ -n "$D" ] || { echo "mysql/mariadb client not found in the container" >&2; exit 127; }`
	}
	opts := `cat` // option file arrives on stdin from the agent
	if useEnv {
		opts = `P="${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-}}"; [ -n "$P" ] || { echo "MYSQL_ROOT_PASSWORD is not set in the container" >&2; exit 2; }; ` +
			`printf '[client]\nuser=%s\npassword="%s"\n' "$U" "$(printf '%s' "$P" | sed 's/\\/\\\\/g; s/"/\\"/g')"`
	}
	return `set -e; U="$1"; shift; ` + find + `; ` + opts + ` | "$D" --defaults-extra-file=/dev/stdin "$@"`
}

// mysqlCommand builds the command for a dump ("dump") or a connection probe ("client").
func (a *Agent) mysqlCommand(ctx context.Context, d proto.DumpConfig, tool string, toolArgs []string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	if d.Container != "" {
		args := []string{"exec", "-i", d.Container, "sh", "-c", containerScript(tool, d.UseContainerEnv), "sh", d.User}
		cmd = exec.CommandContext(ctx, a.cfg.DockerPath, append(args, toolArgs...)...)
	} else {
		names := []string{"mysqldump", "mariadb-dump"}
		if tool == "client" {
			names = []string{"mysql", "mariadb"}
		}
		var bin string
		for _, n := range names {
			if p, err := exec.LookPath(n); err == nil {
				bin = p
				break
			}
		}
		if bin == "" {
			return nil, fmt.Errorf("%s is not installed on this machine (install the mysql or mariadb client, or dump from the Docker container instead)", names[0])
		}
		args := append([]string{"--defaults-extra-file=/dev/stdin", "--protocol=TCP", "-h", d.Host, "-P", fmt.Sprint(d.Port)}, toolArgs...)
		cmd = exec.CommandContext(ctx, bin, args...)
	}
	if !d.UseContainerEnv {
		cmd.Stdin = strings.NewReader(optionFile(d.User, d.Password))
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir()}
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		env = append(env, "DOCKER_HOST="+h)
	}
	cmd.Env = env
	cmd.WaitDelay = 15 * time.Second
	return cmd, nil
}

func dumpWhere(d proto.DumpConfig) string {
	if d.Container != "" {
		return "Docker container " + d.Container
	}
	return fmt.Sprintf("%s:%d", d.Host, d.Port)
}

func dumpWhat(d proto.DumpConfig) string {
	if len(d.Databases) == 0 {
		return "all databases"
	}
	return "database(s) " + strings.Join(d.Databases, ", ")
}

// runDumps handles a job's databases for one run. Returns (ok, failed).
func (a *Agent) runDumps(ctx context.Context, job *proto.JobConfig, mode, stamp string, lg *runLogger) (int, int) {
	ok, failed := 0, 0
	for _, d := range job.Dumps {
		if ctx.Err() != nil {
			break
		}
		switch mode {
		case proto.ModeVerify:
			lg.agent("database %s: skipped by verify (every backup takes a fresh dump)", d.Name)
			continue
		case proto.ModeDryRun:
			res := a.testDump(ctx, d)
			if res.OK {
				lg.agent("database %s: would dump %s from %s (%s, %d database(s) visible)", d.Name, dumpWhat(d), dumpWhere(d), res.Version, len(res.Databases))
				ok++
			} else {
				lg.agent("database %s: connection check FAILED: %s", d.Name, res.Error)
				failed++
			}
			continue
		}
		if err := a.dumpOne(ctx, job, d, stamp, lg); err != nil {
			if ctx.Err() == nil {
				lg.agent("FAILED database %s: %v", d.Name, err)
			}
			failed++
			continue
		}
		ok++
		if d.KeepDays > 0 {
			a.pruneDumps(ctx, job, d, lg)
		}
	}
	return ok, failed
}

func dumpDir(job *proto.JobConfig, agentName string, d proto.DumpConfig) string {
	return agentRoot(job.Wasabi, *job, agentName) + "/_databases/" + unsafeSeg.ReplaceAllString(d.Name, "_")
}

// countingWriter counts bytes and remembers the tail of the stream.
type countingWriter struct {
	w    io.Writer
	n    int64
	tail []byte
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.tail = append(c.tail, p[:n]...)
	if len(c.tail) > 512 {
		c.tail = c.tail[len(c.tail)-512:]
	}
	return n, err
}

func (a *Agent) dumpOne(ctx context.Context, job *proto.JobConfig, d proto.DumpConfig, stamp string, lg *runLogger) error {
	dir := dumpDir(job, a.name(), d)
	final := dir + "/" + unsafeSeg.ReplaceAllString(d.Name, "_") + "-" + stamp + ".sql.gz"
	partial := final + ".partial"
	env := rcloneEnv(job.Wasabi)
	lg.agent("database %s: dumping %s from %s -> %s", d.Name, dumpWhat(d), dumpWhere(d), strings.TrimPrefix(final, ":s3:"))

	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dump, err := a.mysqlCommand(dctx, d, "dump", append(append([]string{}, mysqlDumpFlags...), dumpDBArgs(d)...))
	if err != nil {
		return err
	}
	dumpOut, err := dump.StdoutPipe()
	if err != nil {
		return err
	}
	dumpErr, _ := dump.StderrPipe()

	pr, pw := io.Pipe()
	rcat := exec.CommandContext(dctx, a.cfg.RclonePath, "rcat", partial, "--log-level", "ERROR", "--retries", "1", "--low-level-retries", "10",
		// Streamed uploads have unknown size: rclone caps them at 10,000 chunks, so
		// 32 MiB chunks allow dumps up to ~300 GB compressed (~128 MB RAM in flight).
		"--s3-chunk-size", "32M")
	rcat.Env, rcat.Stdin, rcat.WaitDelay = env, pr, 15*time.Second
	rcatErr, _ := rcat.StderrPipe()

	if err := rcat.Start(); err != nil {
		return fmt.Errorf("start rclone: %w", err)
	}
	if err := dump.Start(); err != nil {
		pw.CloseWithError(err)
		_ = rcat.Wait()
		return fmt.Errorf("start dump (is Docker available to the agent?): %w", err)
	}

	var wg sync.WaitGroup
	pump := func(r io.Reader, stream string) {
		defer wg.Done()
		sc := newLineScanner(r)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				lg.add(stream, t)
			}
		}
		_, _ = io.Copy(io.Discard, r)
	}
	wg.Add(2)
	go pump(dumpErr, "stderr")
	go pump(rcatErr, "stderr")

	// mysqldump -> gzip -> rclone rcat, counting raw SQL and compressed bytes.
	sqlTail := &countingWriter{w: io.Discard}
	upload := &countingWriter{w: pw}
	gz := gzip.NewWriter(upload)
	_, copyErr := io.Copy(io.MultiWriter(gz, sqlTail), dumpOut)
	if copyErr == nil {
		copyErr = gz.Close()
	}
	dumpWait := dump.Wait()
	complete := dumpWait == nil && copyErr == nil && bytes.Contains(sqlTail.tail, []byte(dumpCompleteMarker))
	if complete {
		pw.Close()
	} else {
		pw.CloseWithError(errors.New("dump failed")) // makes rcat abort the upload
	}
	rcatWait := rcat.Wait()
	wg.Wait()

	cleanup := func() {
		c, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		_, _ = a.rcloneOnce(c, env, nil, nil, "deletefile", partial, "--retries", "1")
	}
	switch {
	case ctx.Err() != nil:
		cleanup()
		return ctx.Err()
	case dumpWait != nil:
		cleanup()
		return fmt.Errorf("mysqldump failed: %v", exitText(dumpWait))
	case copyErr != nil:
		cleanup()
		return fmt.Errorf("reading the dump: %v", copyErr)
	case !complete:
		cleanup()
		return errors.New("dump ended without mysqldump's completion marker; refusing to keep a truncated dump")
	case rcatWait != nil:
		cleanup()
		return fmt.Errorf("upload failed: %v", exitText(rcatWait))
	}
	// Only a complete dump gets its final name.
	if _, err := a.rcloneOnce(ctx, env, nil, nil, "moveto", partial, final, "--retries", "3"); err != nil {
		cleanup()
		return fmt.Errorf("finalising upload: %v", err)
	}
	lg.agent("OK database %s: %s of SQL, %s compressed", d.Name, humanBytes(sqlTail.n), humanBytes(upload.n))
	lg.stats.addUpload(upload.n)
	return nil
}

func exitText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit code %d (see the lines above)", ee.ExitCode())
	}
	return err.Error()
}

func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f, i := float64(n), 0
	for f >= 1000 && i < len(units)-1 {
		f /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// pruneDumps deletes dumps older than KeepDays, always keeping the newest one,
// and removes leftover .partial uploads from interrupted runs.
func (a *Agent) pruneDumps(ctx context.Context, job *proto.JobConfig, d proto.DumpConfig, lg *runLogger) {
	dir := dumpDir(job, a.name(), d)
	env := rcloneEnv(job.Wasabi)
	var files []string
	if _, err := a.rcloneOnce(ctx, env, nil, func(l string) { files = append(files, l) }, "lsf", dir, "--files-only"); err != nil {
		lg.agent("database %s: retention could not list old dumps: %v", d.Name, err)
		return
	}
	for _, f := range expiredDumps(files, unsafeSeg.ReplaceAllString(d.Name, "_"), time.Now(), d.KeepDays) {
		lg.agent("database %s: removing old dump %s (older than %d days)", d.Name, f, d.KeepDays)
		if _, err := a.rcloneOnce(ctx, env, nil, nil, "deletefile", dir+"/"+f); err != nil {
			lg.agent("database %s: could not remove %s: %v", d.Name, f, err)
		}
	}
}

// expiredDumps picks dump files older than days (by the timestamp in their
// name), never the newest complete dump; .partial files older than a day go too.
func expiredDumps(files []string, name string, now time.Time, days int) []string {
	type dump struct {
		file string
		t    time.Time
	}
	var complete []dump
	var out []string
	for _, f := range files {
		partial := strings.HasSuffix(f, ".partial")
		base := strings.TrimSuffix(strings.TrimSuffix(f, ".partial"), ".sql.gz")
		if !strings.HasPrefix(base, name+"-") {
			continue // not ours: never touch
		}
		t, err := time.Parse(versionStamp, strings.TrimPrefix(base, name+"-"))
		if err != nil {
			continue
		}
		if partial {
			if now.Sub(t) > 24*time.Hour {
				out = append(out, f)
			}
			continue
		}
		complete = append(complete, dump{f, t})
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].t.Before(complete[j].t) })
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	for i, d := range complete {
		if i < len(complete)-1 && d.t.Before(cutoff) {
			out = append(out, d.file)
		}
	}
	sort.Strings(out)
	return out
}

// testDump connects with the mysql client and reports the server version and
// databases. Used by the dashboard's Test button and by dry runs.
func (a *Agent) testDump(ctx context.Context, d proto.DumpConfig) proto.DumpTestResult {
	t0 := time.Now()
	tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res := proto.DumpTestResult{}
	cmd, err := a.mysqlCommand(tctx, d, "client", []string{"-N", "-B", "-e", "SELECT VERSION(); SHOW DATABASES;"})
	if err != nil {
		res.Error = err.Error()
		return res
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	res.Millis = time.Since(t0).Milliseconds()
	if err != nil {
		msg := lastMeaningfulLine(stderr.String())
		if tctx.Err() != nil {
			msg = "timed out"
		} else if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			msg = "the docker command is not installed on this machine"
		}
		if hint := dbHint(msg); hint != "" {
			msg = hint + " (" + msg + ")"
		}
		res.Error = msg
		return res
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) > 0 {
		res.Version, res.Databases = strings.TrimSpace(lines[0]), []string{}
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" {
				res.Databases = append(res.Databases, l)
			}
		}
	}
	res.OK = true
	return res
}

func dbHint(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "access denied"):
		return "Wrong database user or password"
	case strings.Contains(m, "no such container"):
		return "Container not found (check the name with: docker ps)"
	case strings.Contains(m, "is not running"):
		return "The container is not running"
	case strings.Contains(m, "cannot connect to the docker daemon"), strings.Contains(m, "docker daemon socket"), strings.Contains(m, "docker.sock"):
		return "The agent cannot reach Docker (the native agent needs /var/run/docker.sock; the Docker agent needs it mounted)"
	case strings.Contains(m, "can't connect to mysql server"), strings.Contains(m, "can't connect to server"), strings.Contains(m, "connection refused"):
		return "Cannot reach the database server from this machine"
	case strings.Contains(m, "unknown database"):
		return "That database does not exist"
	case strings.Contains(m, "mysql_root_password is not set"):
		return "The container has no MYSQL_ROOT_PASSWORD; enter the password instead"
	}
	return ""
}
