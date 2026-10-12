# Provenance

nhm reuses code from ntm (github.com/Dicklesworthstone/ntm), under ntm's
licence. The operator ruled on 2026-10-12, after a legal review of the
full licence text: nhm may reuse or derive from ntm's code (ACFS bead
acfs-9hkh, plan-v8 section 10).

The rules:

- Each file taken from ntm keeps ntm's licence: a `LICENSE` file with
  ntm's text, unmodified and with its rider, sits in the file's directory.
  The top-level `LICENSE` is the same text, because nhm as a whole is a
  derivative work of ntm.
- Each taken file starts with a header that names its source path and
  commit, and its local changes.
- This file lists every taken file. Add a line in the same commit that
  takes a file.
- nhm takes code, not architecture: no tmux backend, detection engine,
  TUI, coordinator or task scheduler.
- Each file is ported by hand. Nothing is generated from ntm by a script.

The same rules apply to code from pt and dcg.

## Files from ntm at 9d5ed97796e37fa7b7c89bc3ba4087c144f0102c

| nhm path | ntm path | Local changes |
|---|---|---|
| `LICENSE` | `LICENSE` | none |
| `e2e/fakeagent/LICENSE` | `LICENSE` | none |
| `e2e/fakeagent/main.go` | `e2e/fakeagent/main.go` | provenance header |
| `e2e/fakeagent/main_test.go` | `e2e/fakeagent/main_test.go` | provenance header; the tests that ran ntm's tmux detectors (`internal/agent`, `internal/ratelimit`, `internal/tmux`) are removed; `TestRenderComposerAndGate` is new and checks the screen text |
| `e2e/fakeagent/resize_signal_other.go` | `e2e/fakeagent/resize_signal_other.go` | provenance header |
| `e2e/fakeagent/resize_signal_unix.go` | `e2e/fakeagent/resize_signal_unix.go` | provenance header |
| `internal/robot/contract/LICENSE` | `LICENSE` | none |
| `internal/robot/contract/assertions.go` | `internal/robot/contract/assertions.go` | provenance header |
| `internal/robot/contract/assertions_test.go` | `internal/robot/contract/assertions_test.go` | provenance header |
| `internal/robot/contract/replay.go` | `internal/robot/contract/replay.go` | provenance header |
| `internal/robot/contract/replay_test.go` | `internal/robot/contract/replay_test.go` | provenance header |
| `internal/robot/contract/testdata/replay_manifest.json` | `internal/robot/contract/testdata/replay_manifest.json` | none |
| `internal/store/sqliteutil/LICENSE` | `LICENSE` | none |
| `internal/store/sqliteutil/dsn.go` | `internal/sqliteutil/dsn.go` | provenance header; package comment |
| `internal/store/sqliteutil/dsn_test.go` | `internal/sqliteutil/dsn_test.go` | provenance header |
| `internal/store/history/LICENSE` | `LICENSE` | none |
| `internal/store/history/entry.go` | `internal/history/entry.go` | provenance header; the `OperationID` field (`operation_id`, omitted when empty) |
| `internal/store/history/lock_unix.go` | `internal/history/lock_unix.go` | provenance header |
| `internal/store/history/storage.go` | `internal/history/storage.go` (derived) | the file is under `$XDG_DATA_HOME/nhm`; only `StoragePath`, `Append`, `ReadAll` and `ReadRecent` are kept, without ntm's privacy mode, encryption and redaction |
| `internal/robot/LICENSE` | `LICENSE` | none |
| `internal/robot/types.go` | `internal/robot/types.go`, `robot.go`, `capabilities.go`, `registry.go` (derived) | only the types of the read surfaces nhm serves (`--robot-status`, `-snapshot`, `-activity`, `-tail`, `-capabilities`), with ntm's JSON keys; optional fields nhm cannot fill left out; nhm's extras under `_nhm`; `NewResponse` takes the output format |
| `internal/core/snapshot/LICENSE` | `LICENSE` | none |
| `internal/core/snapshot/robot.go` | `internal/robot/robot.go` (derived: `statusOverall`, `statusHealthScore`, the summary counting) | they read nhm's model and its attention severities instead of ntm's alert and state stores |

## Files from herdr

| nhm path | Source | Local changes |
|---|---|---|
| `internal/herdr/schema/herdr-api.schema.json` | the output of `herdr api schema --json`, herdr 0.9.3, protocol 22 (github.com/herdrdev/herdr, Apache-2.0) | none |
