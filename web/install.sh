#!/bin/sh
# Wasabi Backup agent installer. Served by the dashboard at /install.sh.
#
#   curl -fsSL https://DASHBOARD/install.sh | sudo sh -s -- --url https://DASHBOARD --key wbk_...
#
# Options:
#   --url URL        dashboard URL (required)
#   --key KEY        agent API key from the dashboard (required)
#   --roots LIST     comma-separated directories the dashboard may browse/back up
#                    (default: /home,/root,/etc,/srv,/opt,/var)
#   --uninstall      stop and remove the agent (keeps rclone)
set -eu

RCLONE_VERSION="${RCLONE_VERSION:-1.75.1}"
PREFIX=/usr/local/bin
CONF_DIR=/etc/wasabi-agent
UNIT=/etc/systemd/system/wasabi-agent.service
URL="" KEY="" ROOTS="/home,/root,/etc,/srv,/opt,/var" UNINSTALL=0

say() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --url) URL="${2:-}"; shift 2 ;;
    --key) KEY="${2:-}"; shift 2 ;;
    --roots) ROOTS="${2:-}"; shift 2 ;;
    --uninstall) UNINSTALL=1; shift ;;
    *) die "unknown option: $1" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "run as root (pipe into 'sudo sh')"
[ "$(uname -s)" = Linux ] || die "Linux only"
command -v systemctl >/dev/null 2>&1 || die "systemd is required"

if [ "$UNINSTALL" -eq 1 ]; then
  systemctl disable --now wasabi-agent 2>/dev/null || true
  rm -f "$UNIT" "$PREFIX/wasabi-agent"
  rm -rf "$CONF_DIR"
  systemctl daemon-reload
  say "agent removed"
  exit 0
fi

URL="${URL%/}"
case "$URL" in http://*|https://*) ;; *) die "--url must start with https:// (or http://)" ;; esac
case "$KEY" in wbk_*) ;; *) die "--key must be the wbk_... key shown by the dashboard" ;; esac
case "$URL" in http://*) printf 'warning: %s is not HTTPS; your API key and Wasabi credentials will cross the network unencrypted\n' "$URL" >&2 ;; esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 RCLONE_ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 RCLONE_ARCH=arm64 ;;
  armv7l|armv7*) ARCH=armv7 RCLONE_ARCH=arm-v7 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac

if command -v curl >/dev/null 2>&1; then fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then fetch() { wget -qO "$2" "$1"; }
else die "curl or wget is required"; fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# ---- agent binary (from the dashboard) ---------------------------------------
say "downloading agent ($ARCH) from $URL"
fetch "$URL/download/wasabi-agent-linux-$ARCH" "$TMP/wasabi-agent" || die "agent download failed: is $URL reachable?"
fetch "$URL/download/wasabi-agent-linux-$ARCH.sha256" "$TMP/agent.sha256" || die "checksum download failed"
(cd "$TMP" && echo "$(cut -d' ' -f1 agent.sha256)  wasabi-agent" | sha256sum -c - >/dev/null) || die "agent checksum mismatch"
install -m 0755 "$TMP/wasabi-agent" "$PREFIX/wasabi-agent"

# ---- rclone (official release, verified against rclone's SHA256SUMS) ---------
if RCLONE="$(command -v rclone 2>/dev/null)"; then
  say "using existing $($RCLONE version | head -1) at $RCLONE"
else
  command -v unzip >/dev/null 2>&1 || die "unzip is required to install rclone (apt install unzip / dnf install unzip)"
  Z="rclone-v$RCLONE_VERSION-linux-$RCLONE_ARCH"
  say "installing rclone v$RCLONE_VERSION"
  fetch "https://downloads.rclone.org/v$RCLONE_VERSION/$Z.zip" "$TMP/$Z.zip" || die "rclone download failed"
  fetch "https://downloads.rclone.org/v$RCLONE_VERSION/SHA256SUMS" "$TMP/SHA256SUMS" || die "rclone checksum download failed"
  (cd "$TMP" && grep " $Z.zip\$" SHA256SUMS | sha256sum -c - >/dev/null) || die "rclone checksum mismatch"
  unzip -q "$TMP/$Z.zip" -d "$TMP"
  install -m 0755 "$TMP/$Z/rclone" "$PREFIX/rclone"
  RCLONE="$PREFIX/rclone"
fi

# ---- config + service ---------------------------------------------------------
install -d -m 0700 "$CONF_DIR"
umask 077
cat > "$CONF_DIR/agent.env" <<EOF
AGENT_SERVER_URL=$URL
AGENT_API_KEY=$KEY
AGENT_BROWSE_ROOTS=$ROOTS
AGENT_RCLONE_PATH=$RCLONE
EOF

cat > "$UNIT" <<'EOF'
[Unit]
Description=Wasabi Backup agent
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/wasabi-agent/agent.env
ExecStart=/usr/local/bin/wasabi-agent
Restart=always
RestartSec=5
# Root, but only allowed to *read* any file: the filesystem is read-only to it
# and every capability except bypassing read permissions is dropped.
CapabilityBoundingSet=CAP_DAC_READ_SEARCH
AmbientCapabilities=CAP_DAC_READ_SEARCH
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
Environment=HOME=/tmp

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable wasabi-agent >/dev/null 2>&1
systemctl restart wasabi-agent
sleep 2
if systemctl is-active --quiet wasabi-agent; then
  say "agent is running; it should appear online in the dashboard"
  say "logs: journalctl -u wasabi-agent -f    uninstall: curl -fsSL $URL/install.sh | sudo sh -s -- --uninstall"
else
  journalctl -u wasabi-agent -n 20 --no-pager >&2 || true
  die "agent failed to start (see logs above)"
fi
