#!/usr/bin/env bash
# nhm's Go surfaces in a real herdr (nhm-qbi.11, plan-v4 section 10, the
# automated cases that need herdr): a private herdr server
# (tests/herdr_lib.sh) with nhm's plugin, [ui] set to the Go surfaces,
# nhm's keys bound, and a fake claude adopted as IcyKnoll. Each script in
# tests/surfaces/ drives an attached herdr client in a pty
# (tests/ptyclient.py) and checks its screen; a track adds its script with
# its surface.
#
# usage: bash tests/surfaces_e2e.sh [SCRIPT...]   (default: every script)
#
# It starts a herdr server, so it runs only in CI or in nhm's bwrap
# sandbox. FAKEAGENT_BIN names a built e2e/fakeagent; without it, the
# test builds one.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=tests/herdr_lib.sh
source "$root/tests/herdr_lib.sh"
herdr_private_guard
trap herdr_private_stop EXIT
herdr_private_start

# nhm owns the server. The takeover needs ACFS's Bash and Agent Mail, which
# the test has not: it writes the owner record that a takeover leaves.
nhm admin instance enroll >/dev/null
inst="$(dirname "$(find "$XDG_STATE_HOME/nhm" -name owner.lock | head -1)")"
printf 'owner = "nhm"\ngeneration = 1\nupdated_at = 2026-10-13T00:00:00Z\n' >"$inst/owner.toml"
printf '[ui]\nmenu = "go"\npalette = "go"\nboard = "go"\n' >"$XDG_CONFIG_HOME/nhm/config.toml"
nhm bind --no-reload >/dev/null 2>&1
herdr server reload-config >/dev/null

# The project and a fake claude in its first pane, adopted.
proj="$HOME/proj"
mkdir -p "$proj"
git -C "$proj" init -q . && git -C "$proj" -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
herdr workspace create --label proj --cwd "$proj" --focus >/dev/null
if [[ -n "${FAKEAGENT_BIN:-}" ]]; then
  cp "$FAKEAGENT_BIN" "$HERDR_WORK/bin/claude"
else
  (cd "$root" && go build -o "$HERDR_WORK/bin/claude" ./e2e/fakeagent)
fi
export FAKE_CONTROL="$HERDR_WORK/fake.ctl"
: >"$FAKE_CONTROL"
herdr pane send-text w1:p1 "cd $proj && claude --control $FAKE_CONTROL --log $HERDR_WORK/fake.log" >/dev/null
herdr pane send-keys w1:p1 enter >/dev/null
for _ in $(seq 1 50); do
  herdr agent list | grep -q '"agent": *"claude"' && break
  sleep 0.2
done
(cd "$proj" && nhm adopt IcyKnoll --pane w1:p1 >/dev/null)

scripts=("$@")
if [[ ${#scripts[@]} -eq 0 ]]; then
  scripts=("$root"/tests/surfaces/*.script)
fi
rc=0
for s in "${scripts[@]}"; do
  echo "== ${s##*/}"
  python3 -I "$root/tests/ptyclient.py" "$s" "$HERDR_WORK/${s##*/}.raw" -- herdr 2>"$HERDR_WORK/${s##*/}.err" || {
    rc=1
    cat "$HERDR_WORK/${s##*/}.err" >&2
  }
done
if [[ "$rc" != 0 ]]; then
  echo "surfaces_e2e: FAIL" >&2
  exit 1
fi
echo "surfaces_e2e: ok"
