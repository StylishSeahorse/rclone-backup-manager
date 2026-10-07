# Wasabi Backup Manager

A Docker-deployable backup system for Linux: a **central web dashboard** where you browse a
machine's files, pick what to back up and set schedules, and a lightweight **agent** on each
machine that runs [rclone](https://rclone.org) against **Wasabi S3** and reports back.

* Server and agent are Go; both are single static binaries (`CGO_ENABLED=0`, pure-Go SQLite).
* The UI is Tailwind CSS + a dependency-free JS app, embedded in the server binary (one image, no Node at runtime).
* The agent image embeds the official `rclone` binary and drives it through its CLI.

> The earlier Python/React prototype in `backend/` and `frontend/` is untouched but no longer wired into
> `docker-compose.yml`.

## Quick start

```bash
cp .env.example .env
# fill in the three secrets (commands are in the file):
#   SERVER_SECRET=$(openssl rand -hex 32)   ADMIN_PASSWORD=...   AGENT_API_KEY=wbk_$(openssl rand -hex 32)
docker compose up -d --build
open http://localhost:8080
```

1. Sign in with `ADMIN_USER` / `ADMIN_PASSWORD`.
2. **Credentials** → add your Wasabi access key, secret, region and bucket.
3. **Agents** → `demo-agent` (pre-enrolled from `AGENT_API_KEY`) is online → *Browse & configure*.
4. Tick folders in the live file tree (the demo agent sees `./demo-data` at `/data`), choose the credentials,
   set a schedule, *Create job*, then *Run now* and watch the log stream in **History**.

To back up real directories, mount them **read-only** into the agent (see the commented examples in
`docker-compose.yml`): `- /home:/host/home:ro`. Anything under `/host` or `/data` appears in the file tree.

## Installing the agent on a device

In the dashboard, open **Agents → Add agent**. It shows a ready-to-paste command:

```bash
curl -fsSL https://backup.example.com/install.sh | sudo sh -s -- --url https://backup.example.com --key wbk_…
```

Run it on any Linux machine with systemd (x86_64, arm64 or armv7, so Raspberry Pis work too). It:

1. downloads the ~7 MB agent binary for the machine's CPU from the dashboard and verifies its checksum;
2. installs rclone from the official release (checksum-verified), or reuses an existing `rclone`;
3. writes `/etc/wasabi-agent/agent.env` (mode 0600) and a `wasabi-agent` systemd service that runs with a
   read-only view of the filesystem and only the `CAP_DAC_READ_SEARCH` capability;
4. starts it. The device appears **online** in the dashboard within seconds.

Options: `--roots /data,/mnt/photos` sets what the dashboard may browse (default
`/home,/root,/etc,/srv,/opt,/var/www`; missing ones are skipped). Rerunning the command upgrades or reconfigures.
Remove it with `curl -fsSL https://backup.example.com/install.sh | sudo sh -s -- --uninstall`.
Logs: `journalctl -u wasabi-agent -f`. The device needs `curl` (or `wget`), plus `unzip` if rclone isn't installed.

Prefer Docker on the device? Use `docker-compose.agent.yml` with `AGENT_SERVER_URL` and `AGENT_API_KEY` instead.

For production, put the dashboard behind HTTPS (set `COOKIE_SECURE=true`): the install command and the agent's
traffic carry its API key.

## Architecture

```
 Browser ──HTTPS──▶ ┌──────────────── Dashboard (Go) ───────────────┐
  (admin, JWT       │  REST API · embedded UI · SQLite · AES-GCM    │
   cookie)          │  Hub: one WebSocket tunnel per agent          │
                    └───────▲───────────────────────▲───────────────┘
                            │ outbound only         │ HTTPS + API key
                            │ (WebSocket, API key)  │ (config, run status, logs)
                    ┌───────┴───────────────────────┴───────────────┐
                    │              Agent container (Go)             │
                    │  scanner ─ cron scheduler ─ runner ──▶ rclone ──▶ Wasabi S3
                    └──────────────────▲────────────────────────────┘
                                       │ bind mounts, read-only
                                  host dirs (/host/…, /data)
```

**Secure file browsing.** The agent dials *out* to the dashboard and keeps one WebSocket open, so it works behind
NAT/firewalls with no inbound port. When an admin expands a folder in the UI, the browser calls
`GET /api/agents/{id}/browse?path=…`; the dashboard relays it down the agent's tunnel as a `browse` RPC; the agent's
scanner (`internal/agent/browse.go`, `os` + `path/filepath`) answers with JSON entries
(`name, path, is_dir, size, mod_time, mode, symlink`). The scanner is the trust boundary:

* only paths under the configured roots (`AGENT_BROWSE_ROOTS`, i.e. what you mounted) are visible;
* every path is cleaned **and symlink-resolved** before the containment check, so `..`, sibling-prefix names
  (`/data-evil`) and symlinks to `/etc` are rejected; symlinks are listed but never reported as directories;
* listings are capped (5000 entries), and the same validation is re-run at backup time, so a compromised
  dashboard still cannot make the agent read outside its mounts.

**Backup loop.**

1. The agent fetches its assignment (`GET /api/agent/config`: jobs, paths, schedules, **decrypted** Wasabi
   credentials) on connect, whenever the dashboard pushes `config_changed`, and every 5 min as a safety net.
2. Its own `robfig/cron` scheduler (no host cron) fires jobs on the schedules defined in the dashboard
   (5-field cron, `@every 6h`, per-schedule time zone). *Run now* and *Cancel* arrive over the same tunnel.
3. For each path the runner executes `rclone copy|sync|copyto` to
   `:s3:<bucket>/<prefix>/<agent>/<absolute path>`. Credentials go only into the child process environment
   (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `RCLONE_S3_*`), with `RCLONE_CONFIG=/dev/null`. They are never on
   the command line, never in a config file, never on disk, and the agent's own environment (including its API
   key) is not inherited by rclone.
4. rclone's stdout/stderr are read line by line (`\r` progress refreshes included), redacted, batched about once a
   second and POSTed to the dashboard with sequence numbers (retries are idempotent). The UI tails them live.
   Start/finish status and exit code are recorded per run; runs that go silent are marked failed after 10 min.

`copy` never deletes at the destination; `sync` mirrors deletions, so it is opt-in per path.

### Authentication

| Channel | Mechanism |
|---|---|
| Admin ↔ dashboard | bcrypt password → HS256 JWT in an `HttpOnly; SameSite=Strict` cookie (12 h); writes also need `X-Requested-With`; login is rate-limited |
| Agent ↔ dashboard | persistent API key (`wbk_` + 256 random bits) as a bearer token for both the WebSocket and the HTTPS calls; only its SHA-256 is stored; *Rotate key* revokes instantly and drops the live tunnel |
| Wasabi secrets at rest | AES-256-GCM (key derived from `SERVER_SECRET`, bound to the row id); never returned by any API |

Agents can only touch their own jobs/runs; an admin JWT is not accepted on agent endpoints and vice versa.

## Database schema

`internal/server/schema.sql` (portable SQLite/PostgreSQL: UUID text keys, epoch-integer timestamps):

```
users(id, username, password_hash, created_at)
agents(id, name, api_key_hash, hostname, version, os, rclone_ver, browse_roots, last_seen_at, created_at)
wasabi_credentials(id, name, access_key, secret_key_enc, region, bucket, endpoint, created_at)
backup_jobs(id, agent_id→agents, credential_id→wasabi_credentials, name, dest_prefix, enabled, …)
backup_paths(id, job_id→backup_jobs, path, mode['copy'|'sync'])        -- selected in the file tree
schedules(id, job_id→backup_jobs, cron_expr, timezone, enabled)        -- many per job
runs(id, job_id, agent_id, trigger, status, exit_code, summary, started_at, finished_at, updated_at)
run_logs(run_id→runs, seq, ts, stream['stdout'|'stderr'|'agent'], line) -- PK (run_id, seq)
agent_config_rev(agent_id, rev)                                          -- change counter driving config pushes
```

## Configuration

**Server** (env): `SERVER_SECRET` (≥32 chars, back it up), `ADMIN_USER`, `ADMIN_PASSWORD`, `LISTEN` (`:8080`),
`DATA_DIR` (`/data`), `COOKIE_SECURE`, `AGENT_DIST_DIR` (`/dist`, agent binaries for the installer), `BOOTSTRAP_AGENT_NAME` / `BOOTSTRAP_AGENT_KEY` (optional pre-enrolment).

**Agent** (env): `AGENT_SERVER_URL`, `AGENT_API_KEY` or `AGENT_API_KEY_FILE`, `AGENT_BROWSE_ROOTS` (`/host,/data`),
`AGENT_MAX_CONCURRENT` (1), `AGENT_CA_FILE` (private CA), `AGENT_HOSTNAME`, `AGENT_RCLONE_PATH`.

**Endpoints.** Wasabi's endpoint is derived from the region (`s3.<region>.wasabisys.com`). A credential may override
it with any S3-compatible endpoint (e.g. MinIO); non-`wasabisys.com` endpoints automatically use rclone's generic
provider with path-style addressing.

**Container hardening** (`docker-compose.yml`): sources mounted `:ro`; agent runs with `cap_drop: ALL` +
`DAC_READ_SEARCH` only (read any mounted file, nothing else), `no-new-privileges`, read-only root FS; server is
distroless, non-root, read-only root FS. To limit the agent to what one user can read, set `user: "uid:gid"`.

## Development

```bash
make test         # go vet + go test -race
make build        # bin/server, bin/agent
make css          # rebuild web/static/tailwind.css (needs Node; the generated file is committed)
make docker       # build both images
```

Go ≥ 1.26 is required (current `modernc.org/sqlite` / `x/crypto`). The Dockerfiles use `golang:1.26-alpine`.

## Known limits / production notes

* **Agent offline restarts:** the agent keeps its config (and credentials) in memory only, so after a container
  restart no scheduled job runs until it can reach the dashboard again. This is the price of never writing
  secrets to disk.
* **Terminate TLS in front of the dashboard** (Caddy, nginx, Traefik). The agent warns when `AGENT_SERVER_URL` is
  plain `http://`. Login throttling keys on the socket IP, so behind a proxy it becomes a shared global limiter
  (fails safe).
* Single dashboard instance (SQLite + in-process hub). Scaling out would need PostgreSQL and a shared message bus
  for the agent tunnels.
* Restore is done with rclone/Wasabi tooling for now; the dashboard covers backup and history.
