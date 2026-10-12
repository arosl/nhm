#!/usr/bin/env bash
# Install an nhm release for the current user.
#
# Usage: install.sh [--version <x.y.z>|latest] [--archive-sha256 [<platform>=]<hex>]...
#
# Downloads nhm-<version>-<platform>.tar.zst and SHA256SUMS from the same
# tagged release of arosl/nhm, checks the archive against SHA256SUMS, and
# unpacks it to ~/.local/share/nhm/releases/<version>/. With
# --archive-sha256, the archive must also have that SHA-256: a pin kept
# outside the release (ACFS keeps one per platform beside its install.sh
# pin), so a re-uploaded archive and SHA256SUMS are refused too. Give it
# once per platform as <platform>=<hex> (linux-amd64=..., linux-arm64=...)
# and the install uses this machine's; a plain <hex> pins whatever this
# machine's archive is. Pins that name no pin for this platform refuse
# the install. The unpacked
# release records its archive's SHA-256 in .archive-sha256; with a pin,
# only a release unpacked from the pinned archive counts as installed,
# and any other is fetched again and replaced. Then it points
# ~/.local/share/nhm/current at that release, and ~/.local/bin/nhm and
# ~/.local/bin/nhm-hook at current/nhm and current/nhm-hook (an older
# release without nhm-hook gets no nhm-hook link). Running it again for
# the same version only re-points the links. Messages go to stderr.
#
# Environment:
#   NHM_VERSION       the version, when --version is not given
#   NHM_ARCHIVE_SHA256 the pins, separated by spaces, when no --archive-sha256 is given
#   NHM_RELEASE_BASE  the release download base (tests use a file:// URL);
#                     default https://github.com/arosl/nhm/releases/download
#   XDG_DATA_HOME     default ~/.local/share
#   NHM_BIN_DIR       default ~/.local/bin
set -euo pipefail

repo_url="https://github.com/arosl/nhm"
base="${NHM_RELEASE_BASE:-$repo_url/releases/download}"
version="${NHM_VERSION:-latest}"
# NHM_ARCHIVE_SHA256 takes the same forms, separated by spaces.
read -ra pins <<< "${NHM_ARCHIVE_SHA256:-}"
pins_given=false
data_home="${XDG_DATA_HOME:-$HOME/.local/share}"
bin_dir="${NHM_BIN_DIR:-$HOME/.local/bin}"

log() { printf 'nhm install: %s\n' "$*" >&2; }
die() {
  log "error: $*"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      [[ $# -ge 2 ]] || die "--version needs a value"
      version="$2"
      shift 2
      ;;
    --version=*)
      version="${1#--version=}"
      shift
      ;;
    --archive-sha256)
      [[ $# -ge 2 ]] || die "--archive-sha256 needs a value"
      [[ "$pins_given" == true ]] || pins=()
      pins_given=true
      pins+=("$2")
      shift 2
      ;;
    --archive-sha256=*)
      [[ "$pins_given" == true ]] || pins=()
      pins_given=true
      pins+=("${1#--archive-sha256=}")
      shift
      ;;
    -h | --help)
      # Static text: under `curl ... | bash` there is no script file to read.
      printf '%s\n' "usage: install.sh [--version <x.y.z>|latest] [--archive-sha256 [<platform>=]<hex>]..." \
        "Installs nhm to ~/.local/share/nhm/releases/<version>/ and links ~/.local/bin/nhm." >&2
      exit 0
      ;;
    *) die "unknown argument: $1" ;;
  esac
done

for tool in curl tar zstd sha256sum; do
  command -v "$tool" > /dev/null 2>&1 || die "$tool is required"
done

case "$(uname -s)" in
  Linux) os=linux ;;
  *) die "nhm runs on the herdr server, which is Linux; this is $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "no nhm build for $(uname -m)" ;;
esac
platform="$os-$arch"

# This machine's pin: a <platform>=<hex> for it, else a plain <hex>.
pin=""
plain=""
for p in "${pins[@]}"; do
  p="${p,,}"
  case "$p" in
    *=*)
      [[ "${p#*=}" =~ ^[0-9a-f]{64}$ && "${p%%=*}" =~ ^[a-z0-9]+-[a-z0-9]+$ ]] ||
        die "--archive-sha256 takes <platform>=<64 hex digits>, not '$p'"
      [[ "${p%%=*}" != "$platform" ]] || pin="${p#*=}"
      ;;
    *)
      [[ "$p" =~ ^[0-9a-f]{64}$ ]] || die "--archive-sha256 needs 64 hex digits, not '$p'"
      plain="$p"
      ;;
  esac
done
[[ -n "$pin" ]] || pin="$plain"
if (( ${#pins[@]} > 0 )) && [[ -z "$pin" ]]; then
  die "the --archive-sha256 pins name no archive for $platform; nothing was installed"
fi

if [[ "$version" == latest ]]; then
  # The latest-release URL redirects to .../releases/tag/v<version>.
  location="$(curl -fsSI "$repo_url/releases/latest" | tr -d '\r' | sed -n 's/^[Ll]ocation: //p' | tail -n 1)"
  version="${location##*/tag/v}"
  [[ -n "$location" && "$version" != "$location" ]] || die "cannot find the latest release"
fi
version="${version#v}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]] || die "version '$version' is not x.y.z"

name="nhm-${version}-${platform}"
releases="$data_home/nhm/releases"
target="$releases/$version"

installed=false
if [[ -x "$target/nhm" ]]; then
  if [[ -z "$pin" || "$(cat "$target/.archive-sha256" 2> /dev/null)" == "$pin" ]]; then
    installed=true
    log "nhm $version is already unpacked in $target"
  else
    log "nhm $version in $target is not known to come from the pinned archive; fetching it again"
  fi
fi
if [[ "$installed" == false ]]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf -- "$tmp"' EXIT
  url="$base/v$version"
  log "downloading $name.tar.zst from $url"
  curl -fsSL --retry 3 -o "$tmp/$name.tar.zst" "$url/$name.tar.zst" || die "download failed: $url/$name.tar.zst"
  archive_sha="$(sha256sum "$tmp/$name.tar.zst" | cut -d' ' -f1)"
  if [[ -n "$pin" ]]; then
    [[ "$archive_sha" == "$pin" ]] ||
      die "$name.tar.zst has SHA-256 $archive_sha, not the pinned $pin: the release asset changed; nothing was installed"
    log "archive matches the pinned SHA-256"
  fi
  curl -fsSL --retry 3 -o "$tmp/SHA256SUMS" "$url/SHA256SUMS" || die "download failed: $url/SHA256SUMS"
  line="$(awk -v f="$name.tar.zst" '$2 == f || $2 == "*" f' "$tmp/SHA256SUMS")"
  [[ -n "$line" ]] || die "SHA256SUMS of v$version has no line for $name.tar.zst"
  (cd "$tmp" && printf '%s\n' "$line" | sha256sum -c --quiet - >&2) || die "checksum mismatch for $name.tar.zst"
  log "checksum ok"
  mkdir -p "$releases"
  tar --zstd -xf "$tmp/$name.tar.zst" -C "$tmp"
  # nhm-hook came with the hooks; an older pinned release has none.
  [[ -x "$tmp/$name/nhm" ]] || die "archive has no $name/nhm"
  (cd "$tmp/$name" && sha256sum -c --quiet SHA256SUMS >&2) || die "archive contents do not match its SHA256SUMS"
  printf '%s\n' "$archive_sha" > "$tmp/$name/.archive-sha256"
  # A rename inside one file system is atomic: target is complete or absent.
  mv -T "$tmp/$name" "$target.partial.$$"
  if [[ -e "$target" ]]; then
    # An unpacked release not known to be the pinned one is replaced.
    mv -T "$target" "$target.old.$$"
    mv -T "$target.partial.$$" "$target"
    rm -rf -- "$target.old.$$"
  else
    mv -T "$target.partial.$$" "$target" ||
      die "$target exists but has no nhm binary; move it away and run again"
  fi
fi

ln -sfn "releases/$version" "$data_home/nhm/current.new.$$"
mv -T "$data_home/nhm/current.new.$$" "$data_home/nhm/current"
mkdir -p "$bin_dir"
# nhm-hook is the stable command line of the agent and plugin hooks. A
# release without it gets no link, and a link that would dangle goes.
for cmd in nhm nhm-hook; do
  if [[ -x "$data_home/nhm/current/$cmd" ]]; then
    ln -sfn "$data_home/nhm/current/$cmd" "$bin_dir/$cmd.new.$$"
    mv -T "$bin_dir/$cmd.new.$$" "$bin_dir/$cmd"
    log "linked $bin_dir/$cmd -> $data_home/nhm/current/$cmd"
  elif [[ -L "$bin_dir/$cmd" && "$(readlink "$bin_dir/$cmd")" == "$data_home/nhm/current/$cmd" ]]; then
    rm -f -- "$bin_dir/$cmd"
    log "nhm $version has no $cmd; removed the link $bin_dir/$cmd"
  fi
done
log "installed nhm $version"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) log "note: $bin_dir is not on PATH" ;;
esac
