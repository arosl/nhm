#!/usr/bin/env bash
# The herdr fit check (nhm-qbi.11, plan-v4 section 8): nhm doctor
# --herdr-fit --placements against a private, headless herdr server, with
# nhm's plugin linked as install.sh lays it out.
#
# usage: bash tests/herdr_fit_test.sh
#
# It starts a herdr server, so it runs only in CI (CI=true) or in nhm's
# bwrap sandbox (SANDBOX_INSIDE=1), never next to a herdr server in use.
# HOME, the XDG directories and HERDR_SOCKET_PATH point into a fresh
# temporary directory. HERDR_BIN names the herdr to use; without it, the
# test downloads herdr's pinned release and checks its SHA256. NHM_BIN and
# NHM_HOOK_BIN name nhm's binaries; without them, it builds them.
set -euo pipefail

HERDR_VERSION=0.9.3
HERDR_URL="https://github.com/herdrdev/herdr/releases/download/v${HERDR_VERSION}/herdr-linux-x86_64"
HERDR_SHA256=18a8dc65f1c2fa485884344356dea1cfd911c6f06cf46fa78e193f4087f4dba7

if [[ "${CI:-}" != true && "${SANDBOX_INSIDE:-}" != 1 ]]; then
  echo "herdr_fit_test: it starts a herdr server; run it in CI or in nhm's sandbox harness" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
# A short directory: herdr refuses a socket path longer than sun_path.
work="$(mktemp -d /tmp/nhm-fit.XXXXXX)"
server=""
cleanup() {
  if [[ -n "$server" ]]; then
    "$work/bin/herdr" server stop >/dev/null 2>&1 || true
    wait "$server" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

export HOME="$work/home"
export XDG_CONFIG_HOME="$HOME/.config" XDG_STATE_HOME="$HOME/.local/state" XDG_DATA_HOME="$HOME/.local/share"
export XDG_CACHE_HOME="$HOME/.cache" TMPDIR="$work/tmp"
export HERDR_CONFIG_PATH="$XDG_CONFIG_HOME/herdr/config.toml" HERDR_SOCKET_PATH="$XDG_CONFIG_HOME/herdr/herdr.sock"
mkdir -p "$work/bin" "$TMPDIR" "$XDG_CONFIG_HOME/herdr" "$XDG_STATE_HOME" "$XDG_CACHE_HOME"

if [[ -n "${HERDR_BIN:-}" ]]; then
  cp "$HERDR_BIN" "$work/bin/herdr"
else
  curl -fsSL -o "$work/bin/herdr" "$HERDR_URL"
  echo "$HERDR_SHA256  $work/bin/herdr" | sha256sum -c --quiet -
fi
chmod 755 "$work/bin/herdr"
export PATH="$work/bin:$PATH"

# nhm's plugin directory beside its binaries, so the manifest's ../nhm
# resolves.
data="$XDG_DATA_HOME/nhm"
mkdir -p "$data/plugin"
if [[ -n "${NHM_BIN:-}" ]]; then
  cp "$NHM_BIN" "$data/nhm"
  cp "${NHM_HOOK_BIN:?give NHM_HOOK_BIN with NHM_BIN}" "$data/nhm-hook"
else
  (cd "$root" && go build -o "$data/nhm" ./cmd/nhm && go build -o "$data/nhm-hook" ./cmd/nhm-hook)
fi
cp "$root/plugin/herdr-plugin.toml" "$data/plugin/"

herdr server >"$work/server.log" 2>&1 &
server=$!
for _ in $(seq 1 100); do
  [[ -S "$HERDR_SOCKET_PATH" ]] && herdr workspace list >/dev/null 2>&1 && break
  kill -0 "$server" 2>/dev/null || break
  sleep 0.1
done
if ! herdr workspace list >/dev/null 2>&1; then
  echo "herdr_fit_test: the herdr server did not start:" >&2
  cat "$work/server.log" >&2
  exit 1
fi
herdr plugin link "$data/plugin" >/dev/null
herdr workspace create --label main --cwd "$HOME" --focus >/dev/null

rc=0
"$data/nhm" doctor --herdr-fit --placements || rc=$?
left="$(herdr workspace list | grep -c '"label": *"nhm-fit"' || true)"
if [[ "$left" != 0 ]]; then
  echo "herdr_fit_test: the check left its throwaway workspace open" >&2
  rc=1
fi
if [[ "$rc" != 0 ]]; then
  echo "herdr_fit_test: FAIL (herdr $HERDR_VERSION)" >&2
  exit 1
fi
echo "herdr_fit_test: ok (herdr $HERDR_VERSION)"
