# nhm

nhm is to [herdr](https://herdr.dev) what
[ntm](https://github.com/Dicklesworthstone/ntm) is to tmux. It is the
swarm layer of the
[ACFS fork](https://github.com/arosl/agentic_coding_flywheel_setup): it
spawns named agents, sends them prompts and knows that they arrived, waits
for turns, wakes agents with unread Agent Mail, and recycles and retires
agents. It speaks ntm's command grammar and robot output, so the tools and
lessons that call ntm work with `nhm` in its place.

nhm runs on the herdr server, in the container or VPS. The herdr client
(on a Mac, for example) needs no nhm.

**Status:** early. This release has `nhm version` only. The design is
plan-v8 of ACFS bead acfs-9hkh; the verbs come in releases 0.1 to 0.4.

## Install

ACFS installs nhm through its manifest (`stack.nhm`). By hand:

```bash
curl -fsSL https://raw.githubusercontent.com/arosl/nhm/main/install.sh | bash -s -- --version latest
```

`install.sh` downloads the archive for your platform and `SHA256SUMS` from
the same tagged release, checks the archive, and unpacks it to
`~/.local/share/nhm/releases/<version>/`. It points
`~/.local/share/nhm/current` at that release, and `~/.local/bin/nhm` and
`~/.local/bin/nhm-hook` at `current/nhm` and `current/nhm-hook`.
`nhm-hook` is the command that the agent CLIs' hooks and herdr's plugin
events run. Releases are for Linux on amd64 and arm64.

## Build and test

Go 1.26 or later.

```bash
go build ./cmd/nhm ./cmd/nhm-hook
go vet ./...
go test -race ./...
gofmt -l .                    # prints nothing when the tree is formatted
shellcheck install.sh scripts/*.sh tests/*.sh
bash tests/install_test.sh    # only on a disposable machine, such as CI
```

`internal/herdr/herdrtest` is a stub herdr server for hermetic tests. It
checks every request and result against the pinned protocol-22 schema in
`internal/herdr/schema`. `e2e/fakeagent` is ntm's agent-TUI test fixture.

## Releases

A tag `v<x.y.z>` runs `.github/workflows/release.yml`. It builds
`nhm-<x.y.z>-linux-amd64.tar.zst` and `nhm-<x.y.z>-linux-arm64.tar.zst`
with `scripts/package.sh` and publishes them with `SHA256SUMS`. Releases
fix forward: a bug is fixed by the next release.

## Licence and provenance

nhm reuses code from ntm. `LICENSE` is ntm's licence text, unmodified,
and `PROVENANCE.md` lists every file taken from ntm, with its source path,
commit and local changes.
