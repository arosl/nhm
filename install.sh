#!/usr/bin/env bash
# Install an nhm release for the current user.
#
# Usage: install.sh [--version <x.y.z>|latest]
#
# Downloads nhm-<version>-<platform>.tar.zst and SHA256SUMS from the same
# tagged release of arosl/nhm, checks the archive against SHA256SUMS, and
# unpacks it to ~/.local/share/nhm/releases/<version>/. Then it points
# ~/.local/share/nhm/current at that release, and ~/.local/bin/nhm and
# ~/.local/bin/nhm-hook at current/nhm and current/nhm-hook. Running it again for the same version only re-points the
# links. Messages go to stderr.
#
# Environment:
#   NHM_VERSION       the version, when --version is not given
#   NHM_RELEASE_BASE  the release download base (tests use a file:// URL);
#                     default https://github.com/arosl/nhm/releases/download
#   XDG_DATA_HOME     default ~/.local/share
#   NHM_BIN_DIR       default ~/.local/bin
set -euo pipefail

repo_url="https://github.com/arosl/nhm"
base="${NHM_RELEASE_BASE:-$repo_url/releases/download}"
version="${NHM_VERSION:-latest}"
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
    -h | --help)
      # Static text: under `curl ... | bash` there is no script file to read.
      printf '%s\n' "usage: install.sh [--version <x.y.z>|latest]" \
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

if [[ -x "$target/nhm" ]]; then
  log "nhm $version is already unpacked in $target"
else
  tmp="$(mktemp -d)"
  trap 'rm -rf -- "$tmp"' EXIT
  url="$base/v$version"
  log "downloading $name.tar.zst from $url"
  curl -fsSL --retry 3 -o "$tmp/$name.tar.zst" "$url/$name.tar.zst" || die "download failed: $url/$name.tar.zst"
  curl -fsSL --retry 3 -o "$tmp/SHA256SUMS" "$url/SHA256SUMS" || die "download failed: $url/SHA256SUMS"
  line="$(awk -v f="$name.tar.zst" '$2 == f || $2 == "*" f' "$tmp/SHA256SUMS")"
  [[ -n "$line" ]] || die "SHA256SUMS of v$version has no line for $name.tar.zst"
  (cd "$tmp" && printf '%s\n' "$line" | sha256sum -c --quiet - >&2) || die "checksum mismatch for $name.tar.zst"
  log "checksum ok"
  mkdir -p "$releases"
  tar --zstd -xf "$tmp/$name.tar.zst" -C "$tmp"
  for cmd in nhm nhm-hook; do
    [[ -x "$tmp/$name/$cmd" ]] || die "archive has no $name/$cmd"
  done
  (cd "$tmp/$name" && sha256sum -c --quiet SHA256SUMS >&2) || die "archive contents do not match its SHA256SUMS"
  # A rename inside one file system is atomic: target is complete or absent.
  mv -T "$tmp/$name" "$target.partial.$$"
  mv -T "$target.partial.$$" "$target" ||
    die "$target exists but has no nhm binary; move it away and run again"
fi

ln -sfn "releases/$version" "$data_home/nhm/current.new.$$"
mv -T "$data_home/nhm/current.new.$$" "$data_home/nhm/current"
mkdir -p "$bin_dir"
# nhm-hook is the stable command line of the agent and plugin hooks.
for cmd in nhm nhm-hook; do
  ln -sfn "$data_home/nhm/current/$cmd" "$bin_dir/$cmd.new.$$"
  mv -T "$bin_dir/$cmd.new.$$" "$bin_dir/$cmd"
done
log "installed nhm $version: $bin_dir/nhm and $bin_dir/nhm-hook -> $data_home/nhm/current/"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) log "note: $bin_dir is not on PATH" ;;
esac
