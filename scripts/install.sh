#!/bin/sh
# OmniToken agent one-click installer.
#
# Downloads the right binary for this OS/arch, verifies it against SHA256SUMS,
# installs it, optionally enrolls the device, and sets it up to run as a
# service (launchd on macOS, systemd on Linux, with a nohup+cron fallback).
#
# Quick start (enroll + install as a service):
#   curl -fsSL <base>/install.sh | OMNITOKEN_ADMIN_TOKEN=… sh -s -- \
#       --server https://ingest.example.net --name "$(hostname)"
#
# Air-gapped hosts that can only reach the hub download from it instead of
# GitHub by pointing --base-url (or OMNITOKEN_BASE_URL) at the hub:
#   … sh -s -- --base-url https://ingest.example.net/agent --server https://ingest.example.net …
#
# Flags:
#   --server URL           hub base URL (enables enrollment)
#   --name NAME            device display name (default: hostname)
#   --resolve-ip IP        pin the hub host's DNS to IP (ADR-0026 §3)
#   --allow-insecure-http  permit plaintext HTTP to a non-loopback hub (mesh/overlay)
#   --base-url URL         where to fetch binaries + SHA256SUMS
#   --prefix DIR           install directory (default: /usr/local/bin as root, else ~/.local/bin)
#   --no-enroll            download + install only, skip enrollment
#   --no-service           skip service setup
#
# Env: OMNITOKEN_ADMIN_TOKEN (enrollment credential), OMNITOKEN_BASE_URL, OMNITOKEN_DEVICE_TOKEN.
set -eu

REPO_BASE_DEFAULT="https://github.com/SuooL/OmniToken/releases/latest/download"
BASE_URL="${OMNITOKEN_BASE_URL:-$REPO_BASE_DEFAULT}"
SERVER=""; NAME=""; RESOLVE_IP=""; ALLOW_INSECURE=""; NO_ENROLL=""; NO_SERVICE=""; PREFIX=""

die() { echo "install: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
		--server) SERVER="${2:-}"; shift 2 ;;
		--name) NAME="${2:-}"; shift 2 ;;
		--resolve-ip) RESOLVE_IP="${2:-}"; shift 2 ;;
		--allow-insecure-http) ALLOW_INSECURE=1; shift ;;
		--base-url) BASE_URL="${2:-}"; shift 2 ;;
		--prefix) PREFIX="${2:-}"; shift 2 ;;
		--no-enroll) NO_ENROLL=1; shift ;;
		--no-service) NO_SERVICE=1; shift ;;
		-h|--help) sed -n '2,30p' "$0" 2>/dev/null || echo "see script header"; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done

# --- detect platform ---------------------------------------------------------
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
	linux) OS=linux ;;
	darwin) OS=darwin ;;
	*) die "unsupported OS: $os (linux and darwin only)" ;;
esac
arch=$(uname -m)
case "$arch" in
	x86_64|amd64) ARCH=amd64 ;;
	arm64|aarch64) ARCH=arm64 ;;
	*) die "unsupported architecture: $arch" ;;
esac
ASSET="omnitoken-${OS}-${ARCH}"

# --- helpers -----------------------------------------------------------------
fetch() { # url dest
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		die "need curl or wget"
	fi
}
sha256() { # file -> hash
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
	else die "need sha256sum or shasum"; fi
}

# --- install prefix ----------------------------------------------------------
if [ -z "$PREFIX" ]; then
	if [ "$(id -u)" = "0" ]; then PREFIX=/usr/local/bin; else PREFIX="$HOME/.local/bin"; fi
fi
mkdir -p "$PREFIX"

# --- download + verify + install --------------------------------------------
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
echo "==> downloading $ASSET from $BASE_URL"
fetch "$BASE_URL/$ASSET" "$tmp/omnitoken"
fetch "$BASE_URL/SHA256SUMS" "$tmp/SHA256SUMS"
want=$(awk -v a="$ASSET" '$2==a || $2=="*"a {print $1}' "$tmp/SHA256SUMS" | head -1)
[ -n "$want" ] || die "no checksum for $ASSET in SHA256SUMS"
got=$(sha256 "$tmp/omnitoken")
[ "$want" = "$got" ] || die "checksum mismatch for $ASSET: want $want, got $got"
echo "==> checksum ok"
chmod +x "$tmp/omnitoken"
install -m 0755 "$tmp/omnitoken" "$PREFIX/omnitoken"
echo "==> installed $("$PREFIX/omnitoken" version) to $PREFIX/omnitoken"
BIN="$PREFIX/omnitoken"

# --- config path -------------------------------------------------------------
if [ "$(id -u)" = "0" ]; then CFGDIR=/etc/omnitoken; else CFGDIR="$HOME/.omnitoken"; fi
mkdir -p "$CFGDIR"
CONFIG="$CFGDIR/agent.json"

# --- enroll ------------------------------------------------------------------
if [ -z "$NO_ENROLL" ] && [ -n "$SERVER" ]; then
	[ -n "${OMNITOKEN_ADMIN_TOKEN:-}" ] || die "OMNITOKEN_ADMIN_TOKEN is required to enroll (or pass --no-enroll)"
	set -- agent enroll -config "$CONFIG" -server "$SERVER"
	[ -n "$NAME" ] && set -- "$@" -name "$NAME"
	[ -n "$ALLOW_INSECURE" ] && set -- "$@" -allow-insecure-http
	echo "==> enrolling against $SERVER"
	"$BIN" "$@"
	if [ -n "$RESOLVE_IP" ]; then
		# Pin the hub host's resolution without touching the URL (ADR-0026 §3).
		# Insert as the first property (valid JSON: trailing comma, rest follows).
		# Appending before the closing brace would leave the prior property without
		# its separating comma.
		tmpc=$(mktemp)
		awk -v ip="$RESOLVE_IP" '
			/"resolve_ip"[[:space:]]*:/ {next}
			{print}
			!ins && $0 ~ /^[[:space:]]*\{[[:space:]]*$/ {print "  \"resolve_ip\": \"" ip "\","; ins=1}
		' "$CONFIG" > "$tmpc" && mv "$tmpc" "$CONFIG"
		echo "==> pinned resolve_ip=$RESOLVE_IP"
	fi
	chmod 600 "$CONFIG" 2>/dev/null || true
elif [ -z "$NO_ENROLL" ] && [ -z "$SERVER" ]; then
	echo "==> no --server given; skipping enrollment (binary installed only)"
fi

# --- service -----------------------------------------------------------------
[ -n "$NO_SERVICE" ] && { echo "==> --no-service: done"; exit 0; }
[ -f "$CONFIG" ] || { echo "==> no agent config; skipping service setup"; exit 0; }

svc_launchd() {
	plist="$HOME/Library/LaunchAgents/com.omnitoken.agent.plist"
	mkdir -p "$HOME/Library/LaunchAgents"
	cat > "$plist" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.omnitoken.agent</string>
  <key>ProgramArguments</key><array>
    <string>$BIN</string><string>agent</string><string>-config</string><string>$CONFIG</string>
  </array>
  <key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$CFGDIR/agent.log</string>
  <key>StandardErrorPath</key><string>$CFGDIR/agent.log</string>
</dict></plist>
PL
	launchctl bootout "gui/$(id -u)/com.omnitoken.agent" 2>/dev/null || true
	launchctl bootstrap "gui/$(id -u)" "$plist"
	echo "==> launchd service com.omnitoken.agent loaded"
}

svc_systemd_system() {
	id omnitoken >/dev/null 2>&1 || useradd --system --home-dir /var/lib/omnitoken --shell /usr/sbin/nologin omnitoken
	mkdir -p /var/lib/omnitoken
	chown -R omnitoken:omnitoken "$CFGDIR" /var/lib/omnitoken 2>/dev/null || true
	cat > /etc/systemd/system/omnitoken-agent.service <<UNIT
[Unit]
Description=OmniToken agent
After=network-online.target
Wants=network-online.target
[Service]
User=omnitoken
ExecStart=$BIN agent -config $CONFIG
Restart=always
RestartSec=10
NoNewPrivileges=true
[Install]
WantedBy=multi-user.target
UNIT
	systemctl daemon-reload
	systemctl enable --now omnitoken-agent
	echo "==> systemd service omnitoken-agent enabled"
}

svc_systemd_user() {
	mkdir -p "$HOME/.config/systemd/user"
	cat > "$HOME/.config/systemd/user/omnitoken-agent.service" <<UNIT
[Unit]
Description=OmniToken agent
[Service]
ExecStart=$BIN agent -config $CONFIG
Restart=always
RestartSec=10
[Install]
WantedBy=default.target
UNIT
	systemctl --user daemon-reload
	systemctl --user enable --now omnitoken-agent
	loginctl enable-linger "$(id -un)" 2>/dev/null || \
		echo "   note: could not enable linger; agent stops on logout unless linger is set"
	echo "==> systemd --user service omnitoken-agent enabled"
}

svc_nohup_cron() {
	line="@reboot setsid nohup $BIN agent -config $CONFIG >$CFGDIR/agent.log 2>&1"
	if command -v crontab >/dev/null 2>&1; then
		( crontab -l 2>/dev/null | grep -v 'omnitoken agent' ; echo "$line" ) | crontab - && \
			echo "==> cron @reboot entry added"
	else
		echo "   note: no systemd/cron; agent started for this session only"
	fi
	pkill -f 'omnitoken agent' 2>/dev/null || true
	sleep 1
	setsid nohup "$BIN" agent -config "$CONFIG" >"$CFGDIR/agent.log" 2>&1 &
	echo "==> agent started (nohup)"
}

echo "==> setting up service"
if [ "$OS" = "darwin" ]; then
	svc_launchd
elif [ "$(id -u)" = "0" ] && command -v systemctl >/dev/null 2>&1; then
	svc_systemd_system
elif command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
	svc_systemd_user
else
	svc_nohup_cron
fi

echo "==> done. logs: $CFGDIR/agent.log"
