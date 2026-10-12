#!/usr/bin/env bash
# Test install.sh against a local release. Run it only on a disposable
# machine (CI, or a throwaway container); it installs into a temporary
# HOME, so it never touches the real ~/.local.
#
# Usage: tests/install_test.sh [<dist-dir>]
#
# <dist-dir> holds archives and SHA256SUMS from scripts/package.sh for
# version $NHM_TEST_VERSION (default 0.0.0-test). Without it, the test
# builds them (needs Go).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
version="${NHM_TEST_VERSION:-0.0.0-test}"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT

dist="${1:-}"
if [[ -z "$dist" ]]; then
  dist="$work/dist"
  bash "$root/scripts/package.sh" "$version" "$dist" "linux-$(go env GOARCH)"
fi
dist="$(cd "$dist" && pwd)"

# The release layout install.sh downloads from: <base>/v<version>/<file>.
mkdir -p "$work/releases"
ln -s "$dist" "$work/releases/v$version"

fails=0
pass() { printf 'PASS %s\n' "$1"; }
fail() {
  printf 'FAIL %s\n' "$1"
  fails=$((fails + 1))
}

run_install() {
  HOME="$work/home" XDG_DATA_HOME="" NHM_BIN_DIR="" \
    NHM_RELEASE_BASE="file://$work/releases" \
    bash "$root/install.sh" "$@"
}

home="$work/home"
mkdir -p "$home"

if run_install --version "$version" 2> "$work/log1"; then
  pass "install exits 0"
else
  fail "install exits 0: $(cat "$work/log1")"
fi
out="$("$home/.local/bin/nhm" version --json 2>&1 || true)"
if [[ "$out" == *"\"version\": \"$version\""* ]]; then
  pass "installed nhm reports $version"
else
  fail "installed nhm reports $version: $out"
fi
if [[ "$(readlink "$home/.local/share/nhm/current")" == "releases/$version" ]]; then
  pass "current points at the release"
else
  fail "current points at the release: $(readlink "$home/.local/share/nhm/current")"
fi

if run_install --version "v$version" 2> "$work/log2" && grep -q 'already unpacked' "$work/log2"; then
  pass "a second run for the same version only re-points the links"
else
  fail "second run: $(cat "$work/log2")"
fi

# A tampered archive must be refused before anything is unpacked.
bad="$work/bad"
mkdir -p "$bad"
cp "$dist"/* "$bad/"
for archive in "$bad"/*.tar.zst; do
  printf 'x' >> "$archive"
done
rm "$work/releases/v$version"
ln -s "$bad" "$work/releases/v$version"
home="$work/home2"
mkdir -p "$home"
if HOME="$home" NHM_RELEASE_BASE="file://$work/releases" bash "$root/install.sh" --version "$version" 2> "$work/log3"; then
  fail "tampered archive refused"
elif grep -q 'checksum mismatch' "$work/log3" && [[ ! -e "$home/.local/share/nhm/releases/$version" ]]; then
  pass "tampered archive refused"
else
  fail "tampered archive refused: $(cat "$work/log3")"
fi

if HOME="$home" NHM_RELEASE_BASE="file://$work/releases" bash "$root/install.sh" --version "not-a-version" 2> /dev/null; then
  fail "a bad version is refused"
else
  pass "a bad version is refused"
fi

if [[ $fails -eq 0 ]]; then
  echo "RESULT: pass"
else
  echo "RESULT: $fails failure(s)"
  exit 1
fi
