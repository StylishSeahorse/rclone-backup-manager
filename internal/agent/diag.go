package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/stylishseahorse/rclone-backup-manager/internal/proto"
)

// diagnose answers the dashboard's test requests.
func (a *Agent) diagnose(ctx context.Context, env proto.Envelope) (any, error) {
	switch env.Type {
	case proto.MsgStatus:
		return a.status(), nil
	case proto.MsgTestDump:
		var d proto.DumpConfig
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return nil, errors.New("bad request")
		}
		return a.testDump(ctx, d), nil
	case proto.MsgTestCreds:
		var req proto.CredentialTestRequest
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			return nil, errors.New("bad request")
		}
		tctx, cancel := context.WithTimeout(ctx, 80*time.Second)
		defer cancel()
		return a.testCredentials(tctx, req), nil
	}
	return nil, errors.New("unsupported request")
}

func (a *Agent) status() proto.AgentStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	st := proto.AgentStatus{
		Hostname: a.cfg.Hostname, Version: Version, RcloneVer: a.rclone, BrowseRoots: a.scanner.Roots(),
		RunningJobs: []string{}, UptimeSec: int64(time.Since(a.started).Seconds()),
	}
	if a.conf != nil {
		st.ConfigRev, st.Jobs = a.conf.Revision, len(a.conf.Jobs)
		for _, j := range a.conf.Jobs {
			if j.Enabled {
				st.Schedules += len(j.Schedules)
			}
			if _, ok := a.running[j.ID]; ok {
				st.RunningJobs = append(st.RunningJobs, j.Name)
			}
		}
	}
	sort.Strings(st.RunningJobs)
	return st
}

// testCredentials checks, from this machine's network, that the credentials
// can reach the bucket and (optionally) write, read and delete a tiny object:
// exactly the operations a backup needs.
func (a *Agent) testCredentials(ctx context.Context, req proto.CredentialTestRequest) proto.CredentialTestResult {
	w := req.Wasabi
	env := rcloneEnv(w)
	quick := []string{"--retries", "1", "--low-level-retries", "1", "--contimeout", "10s", "--timeout", "20s"}
	res := proto.CredentialTestResult{OK: true}
	redact := []string{w.SecretKey, w.AccessKey}

	step := func(name string, fn func() (string, error)) bool {
		st := proto.TestStep{Name: name}
		if !res.OK {
			st.Status = "skipped"
			res.Steps = append(res.Steps, st)
			return false
		}
		t0 := time.Now()
		detail, err := fn()
		st.Millis = time.Since(t0).Milliseconds()
		if err != nil {
			st.Status, st.Detail, res.OK = "failed", redactAll(err.Error(), redact), false
		} else {
			st.Status, st.Detail = "ok", detail
		}
		res.Steps = append(res.Steps, st)
		return err == nil
	}

	step("Connect and list bucket", func() (string, error) {
		n := 0
		_, err := a.rcloneOnce(ctx, env, nil, func(line string) { n++ },
			append([]string{"lsf", ":s3:" + w.Bucket, "--max-depth", "1"}, quick...)...)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("reached %s, bucket %q has %d top-level entr%s", w.Endpoint, w.Bucket, n, plural(n, "y", "ies")), nil
	})

	if !req.WriteTest {
		return res
	}
	rnd := make([]byte, 6)
	_, _ = rand.Read(rnd)
	key := path.Join(strings.Trim(req.Prefix, "/"), ".wasabi-backup-test", unsafeSeg.ReplaceAllString(a.name(), "_")+"-"+hex.EncodeToString(rnd)+".txt")
	obj := ":s3:" + w.Bucket + "/" + key
	payload := "wasabi-backup connectivity test " + time.Now().UTC().Format(time.RFC3339) + "\n"

	wrote := step("Write test object", func() (string, error) {
		_, err := a.rcloneOnce(ctx, env, strings.NewReader(payload), nil, append([]string{"rcat", obj}, quick...)...)
		return key, err
	})
	step("Read it back", func() (string, error) {
		var got strings.Builder
		_, err := a.rcloneOnce(ctx, env, nil, func(l string) { got.WriteString(l + "\n") }, append([]string{"cat", obj}, quick...)...)
		if err != nil {
			return "", err
		}
		if got.String() != payload {
			return "", errors.New("content read back does not match what was written")
		}
		return "content matches", nil
	})
	// Clean up whenever the object was written, even if the read failed.
	del := proto.TestStep{Name: "Delete test object", Status: "skipped"}
	if wrote {
		t0 := time.Now()
		_, err := a.rcloneOnce(ctx, env, nil, nil, append([]string{"deletefile", obj}, quick...)...)
		del.Millis = time.Since(t0).Milliseconds()
		if err != nil {
			del.Status, del.Detail, res.OK = "failed", redactAll(err.Error(), redact)+" (remove "+key+" manually)", false
		} else {
			del.Status = "ok"
		}
	}
	res.Steps = append(res.Steps, del)
	return res
}

// rcloneOnce runs a short rclone command, feeding stdout lines to onLine. On
// failure the error is rclone's own last message plus a plain-language hint.
func (a *Agent) rcloneOnce(ctx context.Context, env []string, stdin *strings.Reader, onLine func(string), args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, a.cfg.RclonePath, args...)
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if onLine != nil {
			onLine(sc.Text())
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return 1, errors.New("timed out: no answer from the endpoint")
		}
		msg := lastMeaningfulLine(stderr.String())
		if h := hintFor(msg); h != "" {
			msg = h + " (" + msg + ")"
		}
		return 1, errors.New(msg)
	}
	return 0, nil
}

var logPrefix = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} [A-Z]+\s*:\s*`)

func lastMeaningfulLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(logPrefix.ReplaceAllString(lines[i], ""))
		if l != "" && !strings.HasPrefix(l, "Attempt ") {
			if len(l) > 400 {
				l = l[:400] + "…"
			}
			return l
		}
	}
	return "rclone failed without a message"
}

// hintFor translates common S3 failures into something actionable.
func hintFor(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "invalidaccesskeyid"):
		return "Access key not recognised"
	case strings.Contains(m, "signaturedoesnotmatch"):
		return "Secret key is wrong"
	case strings.Contains(m, "nosuchbucket") || strings.Contains(m, "directory not found"):
		return "Bucket does not exist (check the name and region)"
	case strings.Contains(m, "accessdenied") || strings.Contains(m, "statuscode: 403"):
		return "Access denied: the key lacks permission for this bucket or operation"
	case strings.Contains(m, "permanentredirect") || strings.Contains(m, "authorizationheadermalformed") || strings.Contains(m, "statuscode: 301"):
		return "Bucket is in a different region than configured"
	case strings.Contains(m, "no such host"):
		return "Endpoint host name does not resolve"
	case strings.Contains(m, "connection refused") || strings.Contains(m, "i/o timeout") || strings.Contains(m, "deadline exceeded"):
		return "Cannot connect to the endpoint from this machine (firewall/proxy?)"
	case strings.Contains(m, "x509") || strings.Contains(m, "certificate"):
		return "TLS certificate problem talking to the endpoint"
	case strings.Contains(m, "requesttimetooskewed"):
		return "This machine's clock is wrong; fix NTP"
	}
	return ""
}

func redactAll(s string, secrets []string) string {
	for _, x := range secrets {
		if len(x) >= 4 {
			s = strings.ReplaceAll(s, x, "***")
		}
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
