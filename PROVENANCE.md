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
| `internal/core/ensemble/LICENSE` | `LICENSE` | none |
| `internal/core/ensemble/modes_ntm.go` | `internal/ensemble/modes.go` | provenance header |
| `internal/core/ensemble/presets_ntm.go` | `internal/ensemble/ensembles.go` | provenance header |
| `internal/core/ensemble/card_ntm.go` | `internal/ensemble/mode_card.go` | provenance header; `GetModeCard` is `(*Catalog).ModeCard` on nhm's catalog; complements in the catalog's fixed order |
| `internal/core/ensemble/output_ntm.go` | `internal/cli/modes.go`, `internal/cli/ensemble_presets.go`, `internal/output/text.go` (in part) | provenance header; the modes and presets output types and renderers, and `Table`, exported and built from nhm's catalog with the clock passed in; widths in runes, not lipgloss's; built-in presets' source `embedded` |
| `internal/core/ensemble/output_ntm_test.go` | `internal/cli/modes_test.go`, `internal/cli/ensemble_presets_test.go` (in part) | provenance header; nhm's names; plus nhm's tests of the catalog through these shapes |
| `internal/core/ensemble/types.go` | `internal/ensemble/types.go` (in part) | only the types the catalog and presets use, with JSON tags only; ntm's UI fields (icon, color, preamble key), session, assignment, cache and output types and its TOML/YAML loading left out; `Catalog`, `Mode`, `Preset` and `Resolve` are nhm's |
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
| `internal/robot/send.go` | `internal/robot/robot.go`, `cm_inject.go`, `operation_idempotency.go`, `send_idempotency.go` (derived) | only the send, receipt and operation types nhm fills, with ntm's JSON keys; nhm's receipt fields under `_nhm`; `ErrCodeNotOwner` is nhm's |
| `internal/robot/ack.go` | `internal/robot/ack.go` (derived) | the types only; nhm reads acknowledgment from its receipts and the completion observation |
| `internal/robot/spawn.go` | `internal/robot/spawn.go`, `spawn_pacing.go`, `internal/pressure/pressure.go` (derived) | only the fields nhm fills of `SpawnOutput`, `SpawnedAgent`, `SpawnStaggerPlan`, `SpawnStaggerSlot` and `SpawnAdmission`; nhm's admission reasons and warnings; extras under `_nhm` |
| `internal/core/templates/LICENSE` | `LICENSE` | none |
| `internal/core/templates/template.go` | `internal/templates/template.go` | provenance header; nhm's placeholders (`agent`, `herdr`, `model`, `thread`, `project`, `group`) in `ExecutionContext`; `Template.Enrichment` (plan-v8 2.13) |
| `internal/core/templates/parser.go` | `internal/templates/parser.go` | provenance header; the unused `@macro` pattern removed; nhm's per-target placeholders are pane context and filled in `variables` |
| `internal/core/templates/builtin.go` | `internal/templates/builtin.go` | provenance header |
| `internal/core/templates/parser_test.go` | `internal/templates/parser_test.go` | provenance header; the loader tests use nhm's `NewLoader` and `.nhm/templates` |
| `internal/core/templates/builtin_test.go` | `internal/templates/builtin_test.go` | provenance header |
| `internal/core/snapshot/LICENSE` | `LICENSE` | none |
| `internal/core/snapshot/robot.go` | `internal/robot/robot.go` (derived: `statusOverall`, `statusHealthScore`, the summary counting) | they read nhm's model and its attention severities instead of ntm's alert and state stores |
| `internal/redaction/LICENSE` | `LICENSE` | none |
| `internal/redaction/redaction.go` | `internal/redaction/patterns.go`, `types.go` (derived) | the built-in pattern table, its categories and the literal prefilter only; `Redact` writes `[REDACTED:<CATEGORY>]`, as ACFS's Bash does, without ntm's hash suffix, allowlist, extra patterns and line info; a deliberate strengthening: a private key's whole PEM block is redacted (also one cut off before its END line), where ntm redacts only the header line, and the PEM patterns run before every other; `Find` returns the secrets as spans, for text checked in a folded form, and `Redact` is built on it |
| `internal/ui/tui/agent/LICENSE` | `LICENSE` | none |
| `internal/ui/tui/agent/types.go` | `internal/agent/types.go` (in part) | provenance header; package comment; only `AgentType`, its constants, `String`, `Canonical`, `DisplayName`, `ProfileName` and `IsValid`, for the UI's colours, icons and names; ntm's detection engine (state, recommendations, parser types) and its Grok and delivery checks left out |
| `internal/ui/tui/agent/types_test.go` | `internal/agent/types_test.go` (in part) | provenance header; the tests of the kept functions; `TestNhmKindsAreKnown` is nhm's |
| `internal/ui/tui/theme/LICENSE` | `LICENSE` | none |
| `internal/ui/tui/theme/theme.go` | `internal/tui/theme/theme.go` | provenance header |
| `internal/ui/tui/theme/semantic.go` | `internal/tui/theme/semantic.go` | provenance header; import path |
| `internal/ui/tui/theme/huh.go` | `internal/tui/theme/huh.go` | provenance header |
| `internal/ui/tui/theme/theme_test.go` | `internal/tui/theme/theme_test.go` | provenance header |
| `internal/ui/tui/theme/semantic_test.go` | `internal/tui/theme/semantic_test.go` | provenance header |
| `internal/ui/tui/theme/huh_test.go` | `internal/tui/theme/huh_test.go` | provenance header |
| `internal/ui/tui/theme/configured_test.go` | `internal/tui/theme/configured_test.go` | provenance header |
| `internal/ui/tui/styles/LICENSE` | `LICENSE` | none |
| `internal/ui/tui/styles/styles.go` | `internal/tui/styles/styles.go` | provenance header; import paths; behaviour: `AnimationsEnabled` is false inside herdr (`HERDR_ENV` or `HERDR_SOCKET_PATH`), as in tmux and screen |
| `internal/ui/tui/styles/badges.go` | `internal/tui/styles/badges.go`, `internal/util/text.go` (`FormatBytes`) | provenance header; import paths; `util.FormatBytes` copied in as `formatBytes` |
| `internal/ui/tui/styles/tokens.go` | `internal/tui/styles/tokens.go` | provenance header |
| `internal/ui/tui/styles/styles_test.go` | `internal/tui/styles/styles_test.go` | provenance header; `TestAnimationsEnabledAutoDisablesInHerdr` is nhm's |
| `internal/ui/tui/styles/styles_theme_test.go` | `internal/tui/styles/styles_theme_test.go` | provenance header; import path |
| `internal/ui/tui/styles/styles_branches_test.go` | `internal/tui/styles/styles_branches_test.go` | provenance header |
| `internal/ui/tui/styles/badges_test.go` | `internal/tui/styles/badges_test.go` | provenance header |
| `internal/ui/tui/styles/tokens_test.go` | `internal/tui/styles/tokens_test.go` | provenance header |
| `internal/ui/tui/styles/tokens_extra_test.go` | `internal/tui/styles/tokens_extra_test.go` | provenance header |
| `internal/ui/tui/icons/LICENSE` | `LICENSE` | none |
| `internal/ui/tui/icons/icons.go` | `internal/tui/icons/icons.go` | provenance header; import path |
| `internal/ui/tui/icons/icons_test.go` | `internal/tui/icons/icons_test.go` | provenance header |
| `internal/ui/tui/icons/env_detection_test.go` | `internal/tui/icons/env_detection_test.go` | provenance header |
| `internal/ui/tui/layout/LICENSE` | `LICENSE` | none |
| `internal/ui/tui/layout/layout.go` | `internal/tui/layout/layout.go` | provenance header |
| `internal/ui/tui/layout/layout_test.go` | `internal/tui/layout/layout_test.go` | provenance header |
| `internal/ui/tui/terminal/LICENSE` | `LICENSE` | none |
| `internal/ui/tui/terminal/caps.go` | `internal/tui/terminal/caps.go` | provenance header |
| `internal/ui/tui/components/LICENSE` | `LICENSE` | none |
| `internal/ui/tui/components/banner.go` | `internal/tui/components/banner.go` | provenance header; import paths |
| `internal/ui/tui/components/freshness.go` | `internal/tui/components/freshness.go` | provenance header; import path |
| `internal/ui/tui/components/help.go` | `internal/tui/components/help.go` | provenance header; import paths |
| `internal/ui/tui/components/pane_table.go` | `internal/tui/components/pane_table.go` | provenance header; import path |
| `internal/ui/tui/components/progress.go` | `internal/tui/components/progress.go` | provenance header; import paths |
| `internal/ui/tui/components/scroll.go` | `internal/tui/components/scroll.go` | provenance header; import path |
| `internal/ui/tui/components/scrollable_panel.go` | `internal/tui/components/scrollable_panel.go` | provenance header; import path |
| `internal/ui/tui/components/sparkline.go` | `internal/tui/components/sparkline.go` | provenance header; import path |
| `internal/ui/tui/components/spring_manager.go` | `internal/tui/components/spring_manager.go` | provenance header; import path |
| `internal/ui/tui/components/state.go` | `internal/tui/components/state.go` | provenance header; import paths |
| `internal/ui/tui/components/statusbar.go` | `internal/tui/components/statusbar.go` | provenance header; import paths |
| `internal/ui/tui/components/tabbar.go` | `internal/tui/components/tabbar.go` | provenance header; import paths |
| `internal/ui/tui/components/components_test.go` | `internal/tui/components/components_test.go` | provenance header; import paths |
| `internal/ui/tui/components/freshness_test.go` | `internal/tui/components/freshness_test.go` | provenance header; `status.StripANSI` is `ansi.Strip` (charmbracelet/x/ansi) |
| `internal/ui/tui/components/pane_table_test.go` | `internal/tui/components/pane_table_test.go` | provenance header; import path |
| `internal/ui/tui/components/progress_test.go` | `internal/tui/components/progress_test.go` | provenance header; import path |
| `internal/ui/tui/components/scroll_test.go` | `internal/tui/components/scroll_test.go` | provenance header |
| `internal/ui/tui/components/scrollable_panel_test.go` | `internal/tui/components/scrollable_panel_test.go` | provenance header |
| `internal/ui/tui/components/spring_manager_test.go` | `internal/tui/components/spring_manager_test.go` | provenance header |
| `internal/ui/tui/components/state_test.go` | `internal/tui/components/state_test.go` | provenance header |
| `internal/ui/tui/components/statusbar_test.go` | `internal/tui/components/statusbar_test.go` | provenance header |
| `internal/ui/palette/LICENSE` | `LICENSE` | none |
| `internal/ui/palette/catalog.go` | `internal/config/config.go` (in part) | provenance header; only the `PaletteCmd` and `PaletteState` types, without TOML tags, and `PaletteCmd.Source`; the commands come from nhm's palette catalogs (`templates.ReadCatalog`) in the fzf palette's order, not from ntm's config, without terminal controls |
| `internal/ui/palette/keymap.go` | `internal/palette/keymap.go` | provenance header; import paths. Behaviour: one `TargetKind` binding over nhm's kind digits in place of ntm's per-kind target bindings; `ctrl+o` and `m` show the memory preview; `c` and `s` switch CM and CASS for the sends; nhm's result, variables and Composer keymaps |
| `internal/ui/palette/model.go` | `internal/palette/model.go` | provenance header; import paths; the stand-in types; the title. Behaviour (each listed in the header): panes from nhm's snapshot off the update loop, an older read dropped; `TargetKind` on nhm's kind digits; `Draft` keeps "present" apart from its text, so an emptied draft is refused, never the template; `send` freezes the targets at the keypress and submits once through nhm's send operation, with receipts on a result screen, in place of tmux send-keys and a quit; pins, favourites and recents through `Store`; `intsToStrings` and the history record left out (the core records history); `newEditor`, shared with the Composer; the memory preview and the CM and CASS choice for the sends; a selection another surface hands on is the only target, never widened; text without terminal controls |
| `internal/ui/palette/xf_search.go` | `internal/palette/xf_search.go` | provenance header; import paths; `internal/integrations/xf` in place of ntm's tools; a picked result drops the draft; results shown without terminal controls |
| `internal/ui/palette/selector.go` | `internal/palette/selector.go` | provenance header; import paths; `Session` is a herdr workspace and `Selected` returns its id; the texts name workspaces and `nhm new`; no tuilog redirect |
| `internal/ui/palette/keymap_test.go` | `internal/palette/keymap_test.go` | provenance header; import paths; nhm's phases and the Composer in the honesty table; nhm's target keys |
| `internal/ui/palette/list_helpers_test.go` | `internal/palette/list_helpers_test.go` | provenance header; `TestIntsToStrings` left out with `intsToStrings` |
| `internal/ui/palette/model_helpers_test.go` | `internal/palette/model_helpers_test.go` | provenance header; `PaletteCmd` is this package's |
| `internal/ui/palette/palette_test.go` | `internal/palette/palette_test.go` | provenance header; the stand-in types; the target, send, pin and recents tests on nhm's kind digits, the `Submitter` and the `Store`; the selector returns ids; the tests after "nhm" are nhm's |
| `internal/ui/palette/xf_search_test.go` | `internal/palette/xf_search_test.go` | provenance header; `internal/integrations/xf`'s names; a picked result drops the draft |
| `internal/integrations/xf/LICENSE` | `LICENSE` | none |
| `internal/integrations/xf/xf.go` | `internal/tools/xf.go`, `internal/tools/adapter.go` (in part) | provenance header; only `Detect`, `Search` and their types, without ntm's tool registry, version, capability, health and doctor checks; names without the `XF` prefix; the binary is a field; an `io.LimitReader` in place of `LimitedBuffer` |
| `internal/integrations/xf/xf_test.go` | `internal/tools/xf_test.go` (in part) | provenance header; nhm's names; only the search test; the fake-binary tests are nhm's |
| `internal/integrations/cass/LICENSE` | `LICENSE` | none |
| `internal/integrations/cass/keywords_ntm.go` | `internal/cass/inject.go` (in part) | provenance header; only `ExtractKeywords` and its helpers (`tokenize`, `removeCodeBlocks`, the stop words), unchanged; nhm joins the keywords with OR (`KeywordQuery` in `cass.go`), where ntm's `QueryCASS` joins them with spaces |
| `internal/integrations/cass/keywords_ntm_test.go` | `internal/cass/inject_test.go` (in part) | provenance header; only the tests of those functions, unchanged |
| `internal/ui/dashboard/LICENSE` | `LICENSE` | none |
| `internal/ui/dashboard/keymap.go` | `internal/tui/dashboard/keymap.go` | provenance header; import path; behaviour: the keys with no nhm action are gone (c, ctrl+s, e, u, ctrl+k, ctrl+x, n, w, v, o, a: no dead keys, plan-v4 section 2); nhm's board keys space (actions), x (mark), / (filter) and esc (back) are added, with a help group of their own |
| `internal/ui/dashboard/overlay.go` | `internal/tui/dashboard/overlay.go` | provenance header; import path |
| `internal/ui/dashboard/panels/LICENSE` | `LICENSE` | none |
| `internal/ui/dashboard/panels/panel.go` | `internal/tui/dashboard/panels/panel.go` | provenance header |
| `internal/ui/dashboard/panels/panel_base_test.go` | `internal/tui/dashboard/panels/panel_base_test.go` | provenance header |
| `internal/ui/dashboard/panels/height_helpers_test.go` | `internal/tui/dashboard/panels/height_helpers_test.go` | provenance header |
| `internal/ui/dashboard/panels/attention.go` | `internal/tui/dashboard/panels/attention.go` | provenance header; import paths; boundary edits: a local `Actionability` with ntm's three values in place of `robot.Actionability`; `SourcePane` is a string (slot or pane id); `Cursor` is a string key, so `SelectNearestCursor` and `absInt64` are gone; an item with no time shows no age |
| `internal/ui/dashboard/panels/attention_test.go` | `internal/tui/dashboard/panels/attention_test.go` | provenance header; the local `Actionability`; string cursors and panes; the `SelectNearestCursor` tests are gone, and two tests are nhm's (a removed selection keeps its place; an item with no time has no age) |
| `internal/ui/dashboard/focus.go` | `internal/tui/dashboard/focus.go` | provenance header; import path; boundary edits: the board's own `PanelID` set (agents, attention, history) in `panelIDString` and `syncFocusAnimations`; `buildFocusTargets` asks the board's `visiblePanels`; without a spring manager the focused panel gets the full focus colour; `logFocusf` reads `NHM_DEBUG` and `NTM_DEBUG`; the Model methods take a pointer |
| `internal/ui/dashboard/focus_test.go` | `internal/tui/dashboard/focus_test.go` | provenance header; `TestMouseClickPaneListSetsFocus` is gone (it drives ntm's dashboard Model) |
| `internal/ui/dashboard/panels/history.go` | `internal/tui/dashboard/panels/history.go` | provenance header; import paths (nhm's `internal/store/history`, the same `HistoryEntry`); boundary edits: `SetPanes` takes `HistoryPane` (key, label, agent type) in place of `tmux.Pane`, so `formatHistoryPaneLabel` goes; `internal/ui/tui/agent`'s agent types; the agent filter reads each entry's `AgentTypes` first, and its third value is Antigravity in place of Gemini; `now` is injectable; `PreviewOpen` is new; behaviour: prompts lose their terminal controls, and the empty state and the preview hint name only what the board does |
| `internal/ui/dashboard/panels/history_test.go` | `internal/tui/dashboard/panels/history_test.go` | provenance header; import paths; `HistoryPane` and Antigravity in the agent filter test; two tests are nhm's (the entry's own agent types; clean prompts and an injected now) |
| `internal/ui/forms/LICENSE` | `LICENSE` | none |
| `internal/ui/forms/spawn_wizard.go` | `internal/tui/dashboard/panels/spawn_wizard.go`, `internal/tui/dashboard/panels/panel.go` (`Keybinding`) | provenance header; package; nhm's theme import path; `Keybinding` copied in as `SpawnKeybinding`. Behaviour: one count (0 to 32) and, where the kind takes one, a model per kind of nhm's registry, instead of ntm's fixed four; the quick and minimal methods keep ntm's mix for the kinds the registry has; new steps after the counts: options (a new session's label, tab label, group, kickoff, memory), the review (the action's dry run: admission and the plan), the confirm and the result; the wizard runs spawn.add or spawn.new through the action table and its guard, where ntm's dashboard ran `ntm add`; going back drops the review; a refused review has no confirm; esc while the spawn runs only closes the wizard (evidence spawn_wizard-closure.md); a method's preset fills the counts once, so going back keeps the operator's edits; view: five steps, with " → " between them |
| `internal/ui/forms/spawn_wizard_test.go` | `internal/tui/dashboard/panels/spawn_wizard_test.go` | provenance header; the constructor takes a runner, a context and nhm's kinds; counts per kind |
| `internal/ui/forms/spawn_wizard_flow_test.go` | `internal/tui/dashboard/panels/spawn_wizard_flow_test.go` | provenance header; nhm's steps (counts, options, review); the tests from `TestSpawnWizardReviewThenSpawnOnce` on are nhm's |

## Files from ACFS at 4ba4464fbe5e848f3c41eae87c574fe75ffd1319

ACFS (github.com/arosl/agentic_coding_flywheel_setup) has the same
licence text as ntm; `scripts/LICENSE` is ACFS's, unmodified.

| nhm path | ACFS path | Local changes |
|---|---|---|
| `scripts/LICENSE` | `LICENSE` | none |
| `scripts/nhm-palette` | `scripts/lib/palette.sh` | provenance header; targets from `nhm status`, the send through `nhm send -t FILE.md` (bound agents only, `--var thread`), `NHM_CONTEXT`, nhm's palette and state paths; `bind` and `unbind` removed (now `nhm bind`) |

## Files from herdr

| nhm path | Source | Local changes |
|---|---|---|
| `internal/herdr/schema/herdr-api.schema.json` | the output of `herdr api schema --json`, herdr 0.9.3, protocol 22 (github.com/herdrdev/herdr, Apache-2.0) | none |
