#!/usr/bin/env bash
# The herdr fit check (nhm-qbi.11, plan-v4 section 8): nhm doctor
# --herdr-fit --placements against a private, headless herdr server
# (tests/herdr_lib.sh), with nhm's plugin linked as install.sh lays it
# out.
#
# usage: bash tests/herdr_fit_test.sh
#
# It starts a herdr server, so it runs only in CI (CI=true) or in nhm's
# bwrap sandbox (SANDBOX_INSIDE=1), never next to a herdr server in use.
# HERDR_BIN, NHM_BIN and NHM_HOOK_BIN are as in tests/herdr_lib.sh.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=tests/herdr_lib.sh
source "$root/tests/herdr_lib.sh"
herdr_private_guard
trap herdr_private_stop EXIT
herdr_private_start
herdr workspace create --label main --cwd "$HOME" --focus >/dev/null

rc=0
nhm doctor --herdr-fit --placements || rc=$?
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
