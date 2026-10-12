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
if [[ "$(readlink "$home/.local/bin/nhm-hook")" == "$home/.local/share/nhm/current/nhm-hook" ]] &&
  "$home/.local/bin/nhm-hook" --help > /dev/null 2>&1; then
  pass "nhm-hook is linked and runs"
else
  fail "nhm-hook is linked and runs: $(readlink "$home/.local/bin/nhm-hook")"
fi
cur="$home/.local/share/nhm/current"
if [[ -x "$cur/nhm-palette" ]] && grep -q "^version = \"$version\"" "$cur/plugin/herdr-plugin.toml" &&
  "$cur/nhm-palette" --help > /dev/null 2>&1; then
  pass "the release ships nhm-palette and the herdr plugin, with its version"
else
  fail "the release ships nhm-palette and the herdr plugin: $(ls -R "$cur" 2>&1)"
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

# The herdr plugin herdr links is a real directory that stays (herdr
# stores the resolved path), and its ../nhm and ../nhm-hook follow current.
plug="$home/.local/share/nhm/plugin"
if [[ -d "$plug" && ! -L "$plug" ]] && grep -q "^version = \"$version\"" "$plug/herdr-plugin.toml" &&
  [[ "$(readlink "$plug/../nhm")" == "current/nhm" && "$(readlink "$plug/../nhm-hook")" == "current/nhm-hook" ]] &&
  "$plug/../nhm" version --json > /dev/null 2>&1; then
  pass "the stable plugin dir holds the current manifest; ../nhm and ../nhm-hook follow current"
else
  fail "stable plugin dir: $(ls -la "$plug" "$plug/.." 2>&1)"
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
if HOME="$home" XDG_DATA_HOME="" NHM_BIN_DIR="" NHM_RELEASE_BASE="file://$work/releases" \
  bash "$root/install.sh" --version "$version" 2> "$work/log3"; then
  fail "tampered archive refused"
elif grep -q 'checksum mismatch' "$work/log3" && [[ ! -e "$home/.local/share/nhm/releases/$version" ]]; then
  pass "tampered archive refused"
else
  fail "tampered archive refused: $(cat "$work/log3")"
fi

if HOME="$home" XDG_DATA_HOME="" NHM_BIN_DIR="" NHM_RELEASE_BASE="file://$work/releases" \
  bash "$root/install.sh" --version "not-a-version" 2> /dev/null; then
  fail "a bad version is refused"
else
  pass "a bad version is refused"
fi

# A release older than nhm-hook: it installs, and the nhm-hook link from
# the newer release goes instead of dangling.
old="0.0.0-old"
oldname="nhm-$old-linux-$(go env GOARCH 2> /dev/null || echo amd64)"
newname="nhm-$version-linux-$(go env GOARCH 2> /dev/null || echo amd64)"
mkdir -p "$work/oldsrc" "$work/olddist"
tar --zstd -xf "$dist/$newname.tar.zst" -C "$work/oldsrc"
mv "$work/oldsrc/$newname" "$work/oldsrc/$oldname"
rm "$work/oldsrc/$oldname/nhm-hook" "$work/oldsrc/$oldname/nhm-palette" "$work/oldsrc/$oldname/plugin/herdr-plugin.toml"
rmdir "$work/oldsrc/$oldname/plugin"
(cd "$work/oldsrc/$oldname" && sha256sum nhm LICENSE PROVENANCE.md > SHA256SUMS)
tar --zstd -C "$work/oldsrc" -cf "$work/olddist/$oldname.tar.zst" "$oldname"
(cd "$work/olddist" && sha256sum "$oldname.tar.zst" > SHA256SUMS)
ln -s "$work/olddist" "$work/releases/v$old"
home="$work/home"
if run_install --version "$old" 2> "$work/log4" && [[ ! -e "$home/.local/bin/nhm-hook" && ! -L "$home/.local/bin/nhm-hook" ]] &&
  [[ -x "$home/.local/bin/nhm" ]]; then
  pass "a release without nhm-hook installs and leaves no dangling link"
else
  fail "a release without nhm-hook: $(cat "$work/log4"); link: $(readlink "$home/.local/bin/nhm-hook" || true)"
fi
if [[ ! -e "$home/.local/share/nhm/plugin/herdr-plugin.toml" ]] && grep -q 'has no herdr plugin' "$work/log4"; then
  pass "a release without the plugin removes the stable manifest, so herdr runs no command it lacks"
else
  fail "a release without the plugin: $(cat "$work/log4")"
fi

# --archive-sha256: a pin kept outside the release (acfs-9hkh.42).
rm "$work/releases/v$version"
ln -s "$dist" "$work/releases/v$version"
pin="$(sha256sum "$dist/$newname.tar.zst" | cut -d' ' -f1)"
pinned_install() {
  HOME="$1" XDG_DATA_HOME="" NHM_BIN_DIR="" NHM_RELEASE_BASE="file://$work/releases" \
    bash "$root/install.sh" --version "$version" "${@:2}"
}

home="$work/home-pin"
mkdir -p "$home"
if pinned_install "$home" --archive-sha256 "$pin" 2> "$work/log5" &&
  [[ "$(cat "$home/.local/share/nhm/releases/$version/.archive-sha256")" == "$pin" ]]; then
  pass "the pinned archive installs and its SHA-256 is recorded"
else
  fail "pinned install: $(cat "$work/log5")"
fi
if pinned_install "$home" --archive-sha256 "${pin^^}" 2> "$work/log6" && grep -q 'already unpacked' "$work/log6"; then
  pass "a second pinned run (pin in upper case) only re-points the links"
else
  fail "second pinned run: $(cat "$work/log6")"
fi

home="$work/home-wrongpin"
mkdir -p "$home"
wrong="$(printf '%064d' 0)"
if pinned_install "$home" --archive-sha256 "$wrong" 2> "$work/log7"; then
  fail "a wrong pin is refused"
elif grep -q 'not the pinned' "$work/log7" && [[ ! -e "$home/.local/share/nhm/releases/$version" ]]; then
  pass "a wrong pin is refused, and nothing is unpacked"
else
  fail "a wrong pin is refused: $(cat "$work/log7")"
fi

# A re-uploaded release: another archive with a SHA256SUMS that agrees.
evil="$work/evil"
mkdir -p "$evil"
cp "$dist"/* "$evil/"
printf 'x' >> "$evil/$newname.tar.zst"
(cd "$evil" && sha256sum ./*.tar.zst | sed 's# \./# #' > SHA256SUMS)
rm "$work/releases/v$version"
ln -s "$evil" "$work/releases/v$version"
home="$work/home-evil"
mkdir -p "$home"
if pinned_install "$home" --archive-sha256 "$pin" 2> "$work/log8"; then
  fail "a re-uploaded archive with a matching SHA256SUMS is refused under a pin"
elif grep -q 'the release asset changed' "$work/log8" && [[ ! -e "$home/.local/share/nhm/releases/$version" ]]; then
  pass "a re-uploaded archive with a matching SHA256SUMS is refused under a pin"
else
  fail "re-uploaded archive: $(cat "$work/log8")"
fi
rm "$work/releases/v$version"
ln -s "$dist" "$work/releases/v$version"

# An install from before the pin (no record) is fetched again and replaced.
home="$work/home-old-install"
mkdir -p "$home"
pinned_install "$home" 2> /dev/null
rm "$home/.local/share/nhm/releases/$version/.archive-sha256"
if pinned_install "$home" --archive-sha256 "$pin" 2> "$work/log9" && grep -q 'fetching it again' "$work/log9" &&
  [[ "$(cat "$home/.local/share/nhm/releases/$version/.archive-sha256")" == "$pin" ]] &&
  ! compgen -G "$home/.local/share/nhm/releases/$version.*" > /dev/null &&
  "$home/.local/bin/nhm" version --json > /dev/null; then
  pass "an unpinned install is replaced by the pinned archive, with no leftovers"
else
  fail "unpinned install replaced: $(cat "$work/log9")"
fi

if pinned_install "$work/home-pin" --archive-sha256 "not-hex" 2> /dev/null; then
  fail "a malformed pin is refused"
else
  pass "a malformed pin is refused"
fi

# One pin per platform, as ACFS passes them: the install uses its own.
this="linux-$(go env GOARCH 2> /dev/null || echo amd64)"
other="linux-riscv64"
home="$work/home-platform"
mkdir -p "$home"
if pinned_install "$home" --archive-sha256 "$other=$wrong" --archive-sha256 "$this=$pin" 2> "$work/log10" &&
  [[ "$(cat "$home/.local/share/nhm/releases/$version/.archive-sha256")" == "$pin" ]]; then
  pass "of one pin per platform, the install uses this platform's"
else
  fail "per-platform pins: $(cat "$work/log10")"
fi
home="$work/home-platform-env"
mkdir -p "$home"
if NHM_ARCHIVE_SHA256="$other=$wrong $this=$pin" pinned_install "$home" 2> "$work/log11" &&
  [[ "$(cat "$home/.local/share/nhm/releases/$version/.archive-sha256")" == "$pin" ]]; then
  pass "NHM_ARCHIVE_SHA256 takes the same per-platform pins"
else
  fail "NHM_ARCHIVE_SHA256 pins: $(cat "$work/log11")"
fi
home="$work/home-platform-none"
mkdir -p "$home"
if pinned_install "$home" --archive-sha256 "$other=$pin" 2> "$work/log12"; then
  fail "pins for other platforms only are refused"
elif grep -q "name no archive for $this" "$work/log12" && [[ ! -e "$home/.local/share/nhm/releases/$version" ]]; then
  pass "pins for other platforms only are refused, and nothing is unpacked"
else
  fail "pins for other platforms only: $(cat "$work/log12")"
fi
if pinned_install "$home" --archive-sha256 "$this=short" 2> /dev/null; then
  fail "a malformed platform pin is refused"
else
  pass "a malformed platform pin is refused"
fi

# NHM_ARCHIVE_SHA256 with one pin per line: every line counts, not only
# the first.
home="$work/home-newline"
mkdir -p "$home"
if NHM_ARCHIVE_SHA256="$other=$wrong"$'\n'"$this=$pin" pinned_install "$home" 2> "$work/log13" &&
  [[ "$(cat "$home/.local/share/nhm/releases/$version/.archive-sha256")" == "$pin" ]]; then
  pass "NHM_ARCHIVE_SHA256 takes one pin per line"
else
  fail "NHM_ARCHIVE_SHA256 on several lines: $(cat "$work/log13")"
fi

# Two installs at once: the lock lets one unpack and the other finds it
# unpacked. Both exit 0, and one release is left.
home="$work/home-race"
mkdir -p "$home"
pinned_install "$home" 2> "$work/log14a" &
race_a=$!
pinned_install "$home" 2> "$work/log14b" &
race_b=$!
rc_a=0
wait "$race_a" || rc_a=$?
rc_b=0
wait "$race_b" || rc_b=$?
race_dirs=("$home/.local/share/nhm/releases/$version@"*)
if ((rc_a == 0 && rc_b == 0)) && [[ ${#race_dirs[@]} -eq 1 && -d "${race_dirs[0]}" ]] &&
  ! compgen -G "$home/.local/share/nhm/releases/$version.*" > /dev/null &&
  "$home/.local/bin/nhm" version --json > /dev/null 2>&1; then
  pass "two installs at once both succeed and leave one release"
else
  fail "two installs at once: $rc_a $rc_b, ${race_dirs[*]}: $(cat "$work/log14a" "$work/log14b")"
fi

# What a killed install left is removed by the next one; the live
# release stays.
mkdir -p "$home/.local/share/nhm/releases/$version@0123456789ab.1" "$home/.local/share/nhm/releases/$version.partial.1"
if pinned_install "$home" --archive-sha256 "$pin" 2> "$work/log15" &&
  [[ -d "${race_dirs[0]}" && ! -e "$home/.local/share/nhm/releases/$version@0123456789ab.1" ]] &&
  [[ ! -e "$home/.local/share/nhm/releases/$version.partial.1" ]]; then
  pass "leftovers of a killed install are removed, and the live release stays"
else
  fail "leftovers of a killed install: $(cat "$work/log15"); $(ls "$home/.local/share/nhm/releases")"
fi

# A release unpacked by an install.sh from before the link: a directory.
# A pinned install puts the link in its place, current resolves, and the
# directory goes.
home="$work/home-legacy"
legacy="$home/.local/share/nhm"
mkdir -p "$legacy/releases" "$work/legacy-src"
tar --zstd -xf "$dist/$newname.tar.zst" -C "$work/legacy-src"
mv -T "$work/legacy-src/$newname" "$legacy/releases/$version"
ln -s "releases/$version" "$legacy/current"
if pinned_install "$home" --archive-sha256 "$pin" 2> "$work/log16" &&
  [[ -L "$legacy/releases/$version" && "$(cat "$legacy/releases/$version/.archive-sha256")" == "$pin" ]] &&
  [[ "$(readlink "$legacy/current")" == "releases/$version" ]] &&
  ! compgen -G "$legacy/releases/$version.*" > /dev/null &&
  "$home/.local/bin/nhm-hook" --help > /dev/null 2>&1; then
  pass "a release directory from an older install.sh is replaced by the link"
else
  fail "the older layout: $(cat "$work/log16"); $(ls -l "$legacy/releases")"
fi

# A release directory without an nhm binary is replaced, pinned or not.
home="$work/home-empty"
mkdir -p "$home/.local/share/nhm/releases/$version"
if pinned_install "$home" 2> "$work/log17" &&
  [[ -L "$home/.local/share/nhm/releases/$version" ]] &&
  "$home/.local/bin/nhm" version --json > /dev/null 2>&1; then
  pass "a release directory without an nhm binary is replaced"
else
  fail "a release directory without an nhm binary: $(cat "$work/log17")"
fi

if [[ $fails -eq 0 ]]; then
  echo "RESULT: pass"
else
  echo "RESULT: $fails failure(s)"
  exit 1
fi
