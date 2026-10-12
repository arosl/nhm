#!/usr/bin/env bash
# Build the release archives of nhm.
#
# Usage: scripts/package.sh <version> <out-dir> [<platform>...]
#
# Writes <out-dir>/nhm-<version>-<platform>.tar.zst for each platform
# (default: linux-amd64 linux-arm64) and <out-dir>/SHA256SUMS over the
# archives. install.sh checks an archive against SHA256SUMS of the same
# release. Each archive holds one directory, nhm-<version>-<platform>/,
# with the binary, LICENSE, PROVENANCE.md and a SHA256SUMS of its files.
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <version> <out-dir> [<platform>...]" >&2
  exit 2
fi
version="$1"
out="$2"
shift 2
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]]; then
  echo "package: version '$version' is not semver (no leading v)" >&2
  exit 2
fi
platforms=("$@")
if [[ ${#platforms[@]} -eq 0 ]]; then
  platforms=(linux-amd64 linux-arm64)
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
commit="$(git -C "$root" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf -- "$stage"' EXIT

archives=()
for platform in "${platforms[@]}"; do
  goos="${platform%%-*}"
  goarch="${platform#*-}"
  name="nhm-${version}-${platform}"
  dir="$stage/$name"
  mkdir -p "$dir"
  for cmd in nhm nhm-hook; do
    (
      cd "$root" || exit 1
      CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
        -ldflags "-s -w -X github.com/arosl/nhm/internal/cli.Version=${version} -X github.com/arosl/nhm/internal/cli.Commit=${commit}" \
        -o "$dir/$cmd" "./cmd/$cmd"
    )
  done
  cp "$root/LICENSE" "$root/PROVENANCE.md" "$dir/"
  (cd "$dir" && sha256sum nhm nhm-hook LICENSE PROVENANCE.md > SHA256SUMS)
  tar --zstd -C "$stage" --owner=0 --group=0 --numeric-owner -cf "$out/$name.tar.zst" "$name"
  archives+=("$name.tar.zst")
  echo "package: $out/$name.tar.zst" >&2
done
(cd "$out" && sha256sum "${archives[@]}" > SHA256SUMS)
echo "package: $out/SHA256SUMS" >&2
