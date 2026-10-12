# The ntm compatibility inventory

One row per ntm form that ACFS, its lessons, brenner or ru call (plan-v8
2.12). ntm commit tracked: `9d5ed97796e37fa7b7c89bc3ba4087c144f0102c`
(`NTMCommit` in `internal/cli`).

Rules:

- Accepting a flag and ignoring its behaviour is not compatibility. A form
  is `implemented` only when its behaviour and output are ntm's and a test
  checks them.
- `deferred` names the bead that implements the form. `waived` names the
  operator's ruling.
- Upstream contract tests (ntm's own types and goldens) and caller tests
  (what ACFS, brenner and ru assert) are kept apart. Where a caller expects
  another shape than ntm's tracked type, the row says so.
- `nhm compatibility status` reads this table. Keep one row per line, and
  the disposition as the first word of the last cell.

| Form | Callers | Output | nhm | Test | Disposition |
|---|---|---|---|---|---|
| `--robot-status` | ACFS `swarm_status.sh`, `support.sh`; ru | ntm `StatusOutput`: `schema_id`, `schema_version`, `generated_at`, `system`, `sessions[]` of `StatusSessionHeader`, `summary`; extras under `_nhm` | `internal/cli/robot.go`, `internal/core/snapshot/robot.go` | `TestRobotStatus` | implemented |
| `--robot-snapshot` | robots that bootstrap | ntm `SnapshotOutput` subset: `schema_id`, `ts`, `sessions[].agents[]` with `pane` = slot `N.M`, `summary`, `alerts`, `active_incidents`, `replay_window` (unsupported) | `internal/cli/robot.go` | `TestRobotSnapshot` | implemented |
| `--robot-activity=S` | ru | ntm `ActivityOutput`: `agents[]` with ntm's state enum. ru reads `.panes[0].activity`, an older shape: its driver fixture decides the port | `internal/cli/robot.go` | `TestRobotActivity` | implemented (ru's shape: deferred to port.ru, acfs-9hkh.29) |
| `--robot-tail=S --lines=N` | ru | ntm `TailOutput`: `panes{slot: {type, state, lines[]}}` | `internal/cli/robot.go` | `TestRobotTail` | implemented |
| `--robot-capabilities` | brenner, robots | ntm `CapabilitiesOutput`: `commands[]` with unsupported ones marked `unavailable` and a reason | `internal/cli/robot.go` | `TestRobotCapabilities` | implemented |
| `--robot-format=json` | every robot call | JSON; `auto` is JSON; `toon` is refused with `INVALID_FLAG` | `internal/cli/robot.go` | `TestRobotFormat` | implemented |
| `list [--project P]` | pfr, caam, am ports | nhm's envelope with the bound agents and their native sessions | `internal/cli/status.go` | `TestListVerb` | implemented |
| `status <session>` | lessons, operators | one row per agent; `--json` gives nhm's envelope | `internal/cli/status.go` | `TestStatusVerb` | implemented |
| `version`, `--version` | ACFS doctor, lessons | `nhm <version> (commit, herdr protocol, ntm commit)` | `internal/cli/cli.go` | `TestVersionJSON` | implemented |
| `doctor [--json]` | ACFS doctor, lessons | checks with ok, warn or fail and a fix | `internal/cli/doctor.go` | `TestDoctor` | implemented |
| `--robot-spawn=S …` | ACFS `swarm_launch.sh`, ru, brenner | ntm `SpawnOutput` | — | — | deferred: nhm.spawn (acfs-9hkh.19) |
| `--robot-send=S --panes=P --type=K (--msg=M \| --msg-file=-) --op-id=ID` | ACFS `swarm_packet.sh`, ru | ntm `SendOutput`: `success, session, dry_run, blocked, targets, successful, failed, would_send_to, message_preview, cass_injection, memory_injection, operation{operation_id, payload_sha256, payload_bytes, status, admissions[]}`; the receipt's three facts under `_nhm`. `--with-memory` (true) is refused until nhm.memory. `--panes` takes the slot `N.M` or a herdr pane id, not ntm's pane index: `swarm_packet --pane 1` is refused with `PANE_NOT_FOUND` (fails safe); acfs.callers (acfs-9hkh.27) switches the swarm scripts to slots | `internal/cli/send.go`, `internal/core/send/robot.go` | `TestRobotSendContract` | implemented |
| `--robot-send-receipt=OP` | ACFS `swarm_launch.sh`, `swarm_packet.sh` | ntm `SendReceiptOutput`: `operation` and `outcome{success, targets, successful, failed}`; a possibly submitted target is `OPERATION_OUTCOME_UNKNOWN` | `internal/cli/send.go`, `internal/core/send/robot.go` | `TestRobotSendContract` | implemented |
| `--robot-ack=S [--op-id=ID] [--timeout=D]` | robots | ntm `AckOutput`, from the receipts and the completion observation: `prompt_returned` at a completion, `output_started` at a seen start | `internal/cli/send.go`, `internal/core/send/robot.go` | `TestRobotAck` | implemented |
| `--robot-wait …` | ru | ru's pinned driver contract | — | — | deferred: nhm.wait (acfs-9hkh.12) |
| `spawn <session> --cc=N …`, `add <session> …` | lessons, brenner | spawn result | — | — | deferred: nhm.spawn (acfs-9hkh.19) |
| `send <session> [prompt] (--all \| --cc \| --cod \| --agy \| --pane=N \| -t KEY [--bead ID] [--var k=v] [--file F]) [--dry-run] [--no-cass] [--with-memory=false]` | lessons, brenner | one line per target and the operation id; `--json` gives nhm's envelope with the receipts. `--with-memory`, `--with-cass` and `--cass-check` (true) are refused until nhm.memory (acfs-9hkh.21), `--thread` until nhm.wake | `internal/cli/send.go`, `internal/core/send` | `TestSendVerb` | implemented |
| `wait <session> --until …`, `interrupt <session>` | lessons, ru | one line per agent; `--json` gives nhm's envelope. ntm's `--pane` takes the slot `N.M` | `internal/cli/wait.go`, `internal/cli/interrupt.go`, `internal/core/wait` | `TestWaitVerbStateAndSelection`, `TestInterruptVerb` | implemented |
| `attach`, `new`, `kill`, `zoom`, `resume` | lessons | herdr calls | — | — | deferred: nhm.spawn and nhm.lifecycle (acfs-9hkh.19, .20) |
| `palette`, `bind`, `template list|show` | lessons | the palette | — | — | deferred: nhm.plugin (acfs-9hkh.14) |
| `mail inbox` | the palette | inbox | — | — | deferred: nhm.wake (acfs-9hkh.15) |
| `ensemble …`, `modes …` | lessons | ensembles | — | — | deferred: nhm.ensembles (acfs-9hkh.22) |
| `deps [-v]`, `tutorial` | lessons | tools and a pointer to the lessons | — | — | deferred: nhm.kinds (acfs-9hkh.18) |
| `serve`, the web UI | — | — | — | — | waived: F.19 (2026-10-12) |
