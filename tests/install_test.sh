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
if grep -q "^version = \"$version\"" "$cur/plugin/herdr-plugin.toml" && [[ ! -e "$cur/nhm-palette" ]]; then
  pass "the release ships the herdr plugin, with its version, and no nhm-palette"
else
  fail "the release ships the herdr plugin and no nhm-palette: $(ls -R "$cur" 2>&1)"
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
rm "$work/oldsrc/$oldname/nhm-hook" "$work/oldsrc/$oldname/plugin/herdr-plugin.toml"
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
# unpacked. Both exit 0, and one release is left. A curl that takes a
# second makes the two overlap, so only the lock gives one download and
# one "already unpacked": without it, both download.
slow="$work/slow-bin"
mkdir -p "$slow"
real_curl="$(command -v curl)"
printf '#!/usr/bin/env bash\nsleep 1\nexec %q "$@"\n' "$real_curl" > "$slow/curl"
chmod +x "$slow/curl"
home="$work/home-race"
mkdir -p "$home"
PATH="$slow:$PATH" pinned_install "$home" 2> "$work/log14a" &
race_a=$!
PATH="$slow:$PATH" pinned_install "$home" 2> "$work/log14b" &
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
downloads="$(cat "$work/log14a" "$work/log14b" | grep -c 'downloading' || true)"
unpacked="$(cat "$work/log14a" "$work/log14b" | grep -c 'already unpacked' || true)"
if [[ "$downloads" == 1 && "$unpacked" == 1 ]]; then
  pass "the lock serialises them: one downloads, the other finds the release unpacked"
else
  fail "serialisation: $downloads downloads, $unpacked already unpacked: $(cat "$work/log14a" "$work/log14b")"
fi
# The lock itself: while the test holds it, an install waits and unpacks
# nothing; when the test lets go, the install finishes.
home="$work/home-held"
mkdir -p "$home/.local/share/nhm/releases"
exec 8> "$home/.local/share/nhm/releases/.install.lock"
flock 8
pinned_install "$home" 2> "$work/log14c" &
held=$!
sleep 1
if kill -0 "$held" 2> /dev/null && ! compgen -G "$home/.local/share/nhm/releases/$version@*" > /dev/null; then
  waited=true
else
  waited=false
fi
flock -u 8
exec 8>&-
rc_held=0
wait "$held" || rc_held=$?
if [[ "$waited" == true && "$rc_held" == 0 ]] && "$home/.local/bin/nhm" version --json > /dev/null 2>&1; then
  pass "an install waits while another holds the lock, and finishes after"
else
  fail "the held lock: waited=$waited rc=$rc_held: $(cat "$work/log14c")"
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

# A failed download changes nothing: the release that was there stays,
# and nhm, nhm-hook and current still resolve. The pin makes the install
# fetch again (the release has no record); the release dir has no archive.
home="$work/home-faildl"
mkdir -p "$home" "$work/empty-release"
pinned_install "$home" 2> /dev/null || true
rm -f "$home/.local/share/nhm/releases/$version/.archive-sha256"
before="$(readlink "$home/.local/share/nhm/releases/$version" || true)"
rm "$work/releases/v$version"
ln -s "$work/empty-release" "$work/releases/v$version"
if pinned_install "$home" --archive-sha256 "$pin" 2> "$work/log18"; then
  fail "a failed download exits non-zero"
elif grep -q 'download failed' "$work/log18" &&
  [[ -n "$before" && "$(readlink "$home/.local/share/nhm/releases/$version")" == "$before" ]] &&
  [[ "$(readlink "$home/.local/share/nhm/current")" == "releases/$version" ]] &&
  "$home/.local/bin/nhm" version --json > /dev/null 2>&1 &&
  "$home/.local/bin/nhm-hook" --help > /dev/null 2>&1; then
  pass "a failed download keeps the old release, and nhm and nhm-hook still run"
else
  fail "a failed download: $(cat "$work/log18"); $(ls -l "$home/.local/share/nhm/releases")"
fi
rm "$work/releases/v$version"
ln -s "$dist" "$work/releases/v$version"

# A kill during the move from an older install.sh's release directory
# (install.sh: current goes to the new release, the directory moves aside,
# then releases/<version> becomes the link). The states after each step;
# current resolves in each, and the next install finishes the move.
legacy_state() {
  local step="$2" data="$1/.local/share/nhm" newdir="$version@0123456789ab.4242"
  mkdir -p "$data/releases" "$work/legacy-src-$step"
  tar --zstd -xf "$dist/$newname.tar.zst" -C "$work/legacy-src-$step"
  cp -a "$work/legacy-src-$step/$newname" "$data/releases/$version"
  mv -T "$work/legacy-src-$step/$newname" "$data/releases/$newdir"
  printf '%s\n' "$pin" > "$data/releases/$newdir/.archive-sha256"
  ln -s "releases/$newdir" "$data/current"
  if [[ "$step" != current ]]; then
    mv -T "$data/releases/$version" "$data/releases/$version.old.4242"
  fi
  if [[ "$step" == link ]]; then
    ln -s "$newdir" "$data/releases/$version.new.4242"
  fi
}
for step in current aside link; do
  home="$work/home-killed-$step"
  data="$home/.local/share/nhm"
  legacy_state "$home" "$step"
  if [[ ! -x "$data/current/nhm" ]]; then
    fail "killed after '$step': current does not resolve"
    continue
  fi
  if pinned_install "$home" --archive-sha256 "$pin" 2> "$work/log19-$step" &&
    [[ -L "$data/releases/$version" && "$(cat "$data/releases/$version/.archive-sha256")" == "$pin" ]] &&
    [[ "$(readlink "$data/current")" == "releases/$version" ]] &&
    ! compgen -G "$data/releases/$version.*" > /dev/null &&
    "$home/.local/bin/nhm" version --json > /dev/null 2>&1; then
    pass "an install killed after the '$step' step of the legacy move recovers on the next run"
  else
    fail "killed after '$step': $(cat "$work/log19-$step"); $(ls -l "$data/releases")"
  fi
  # The killed install's release was current while the next run started,
  # so that run keeps it; the run after removes it.
  pinned_install "$home" --archive-sha256 "$pin" 2> /dev/null || true
  left=("$data/releases/$version@"*)
  if [[ ${#left[@]} -eq 1 && "${left[0]}" -ef "$data/releases/$version" ]]; then
    pass "the killed install's release dir is gone a run later (after '$step')"
  else
    fail "killed after '$step', one run later: ${left[*]}"
  fi
done

# Without flock the install stops before it changes anything.
nolock="$work/nolock-bin"
mkdir -p "$nolock"
for tool in bash curl tar zstd sha256sum; do
  ln -s "$(command -v "$tool")" "$nolock/$tool"
done
home="$work/home-noflock"
mkdir -p "$home"
if PATH="$nolock" pinned_install "$home" 2> "$work/log20"; then
  fail "an install without flock is refused"
elif grep -q 'flock is required' "$work/log20" && [[ ! -e "$home/.local" ]]; then
  pass "an install without flock stops before any change"
else
  fail "an install without flock: $(cat "$work/log20"); $(ls -A "$home")"
fi

if [[ $fails -eq 0 ]]; then
  echo "RESULT: pass"
else
  echo "RESULT: $fails failure(s)"
  exit 1
fi
