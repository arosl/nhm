# shellcheck shell=bash
# A private, headless herdr server for nhm's end-to-end tests
# (herdr_fit_test.sh, surfaces_e2e.sh). Source it, then:
#
#   herdr_private_guard        refuse to run outside CI or nhm's sandbox
#   herdr_private_start        a fresh HOME, XDG directories and socket under
#                              a temporary directory ($HERDR_WORK); herdr
#                              (HERDR_BIN, else the pinned release, its
#                              SHA256 checked); nhm and nhm-hook (NHM_BIN and
#                              NHM_HOOK_BIN, else built) beside nhm's plugin
#                              as install.sh lays it out ($NHM_DATA); herdr's
#                              first-run screens off; the server started and
#                              the plugin linked. A test may define
#                              herdr_private_before_server: it runs just
#                              before the server starts, so the server
#                              inherits what it exports
#   herdr_private_stop         stop the server and remove $HERDR_WORK
#
# It never touches a herdr server it did not start.

HERDR_VERSION=0.9.3
HERDR_URL="https://github.com/herdrdev/herdr/releases/download/v${HERDR_VERSION}/herdr-linux-x86_64"
HERDR_SHA256=18a8dc65f1c2fa485884344356dea1cfd911c6f06cf46fa78e193f4087f4dba7

HERDR_LIB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HERDR_WORK=""
HERDR_SERVER=""

herdr_private_guard() {
  if [[ "${CI:-}" != true && "${SANDBOX_INSIDE:-}" != 1 ]]; then
    echo "${0##*/}: it starts a herdr server; run it in CI or in nhm's sandbox harness" >&2
    exit 2
  fi
}

herdr_private_stop() {
  if [[ -n "$HERDR_SERVER" ]]; then
    timeout 10 "$HERDR_WORK/bin/herdr" server stop >/dev/null 2>&1 || true
    # A server that does not stop gets SIGKILL after 10 s. The watchdog is
    # killed once wait returns, so it never signals a pid used again.
    (sleep 10 && kill -9 "$HERDR_SERVER" 2>/dev/null) &
    local dog=$!
    wait "$HERDR_SERVER" 2>/dev/null || true
    kill "$dog" 2>/dev/null || true
    HERDR_SERVER=""
  fi
  # go build makes its module cache in the private HOME read-only.
  [[ -z "$HERDR_WORK" ]] || { chmod -R u+w "$HERDR_WORK" 2>/dev/null; rm -rf "$HERDR_WORK"; }
}

herdr_private_start() {
  # A short directory: herdr refuses a socket path longer than sun_path.
  HERDR_WORK="$(mktemp -d /tmp/nhm-e2e.XXXXXX)"
  export HERDR_WORK HOME="$HERDR_WORK/home"
  export XDG_CONFIG_HOME="$HOME/.config" XDG_STATE_HOME="$HOME/.local/state" XDG_DATA_HOME="$HOME/.local/share"
  export XDG_CACHE_HOME="$HOME/.cache" TMPDIR="$HERDR_WORK/tmp"
  export HERDR_CONFIG_PATH="$XDG_CONFIG_HOME/herdr/config.toml" HERDR_SOCKET_PATH="$XDG_CONFIG_HOME/herdr/herdr.sock"
  mkdir -p "$HERDR_WORK/bin" "$TMPDIR" "$XDG_CONFIG_HOME/herdr" "$XDG_CONFIG_HOME/nhm" "$XDG_STATE_HOME" "$XDG_CACHE_HOME"

  if [[ -n "${HERDR_BIN:-}" ]]; then
    cp "$HERDR_BIN" "$HERDR_WORK/bin/herdr"
  else
    curl -fsSL -o "$HERDR_WORK/bin/herdr" "$HERDR_URL"
    echo "$HERDR_SHA256  $HERDR_WORK/bin/herdr" | sha256sum -c --quiet -
  fi
  chmod 755 "$HERDR_WORK/bin/herdr"
  export PATH="$HERDR_WORK/bin:$PATH"

  # nhm's plugin directory beside its binaries, so the manifest's ../nhm
  # resolves.
  export NHM_DATA="$XDG_DATA_HOME/nhm"
  mkdir -p "$NHM_DATA/plugin"
  if [[ -n "${NHM_BIN:-}" ]]; then
    cp "$NHM_BIN" "$NHM_DATA/nhm"
    cp "${NHM_HOOK_BIN:?give NHM_HOOK_BIN with NHM_BIN}" "$NHM_DATA/nhm-hook"
  else
    (cd "$HERDR_LIB_ROOT" && go build -o "$NHM_DATA/nhm" ./cmd/nhm && go build -o "$NHM_DATA/nhm-hook" ./cmd/nhm-hook)
  fi
  cp "$HERDR_LIB_ROOT/plugin/herdr-plugin.toml" "$NHM_DATA/plugin/"
  ln -sf "$NHM_DATA/nhm" "$HERDR_WORK/bin/nhm"

  # No welcome or settings screen for an attached client. herdr's config:
  # "Missing also shows onboarding; set false after you've chosen".
  printf 'onboarding = false\n' >"$HERDR_CONFIG_PATH"
  if declare -F herdr_private_before_server >/dev/null; then
    herdr_private_before_server
  fi
  herdr server >"$HERDR_WORK/server.log" 2>&1 &
  HERDR_SERVER=$!
  local _
  for _ in $(seq 1 100); do
    [[ -S "$HERDR_SOCKET_PATH" ]] && herdr workspace list >/dev/null 2>&1 && break
    kill -0 "$HERDR_SERVER" 2>/dev/null || break
    sleep 0.1
  done
  if ! herdr workspace list >/dev/null 2>&1; then
    echo "herdr_lib: the herdr server did not start:" >&2
    cat "$HERDR_WORK/server.log" >&2
    return 1
  fi
  herdr plugin link "$NHM_DATA/plugin" >/dev/null
}
