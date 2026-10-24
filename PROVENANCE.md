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
- nhm takes ntm's code only where it decides what the user sees (the
  drop-in rule, rule 4 in `AGENTS.md`). The code underneath is nhm's:
  herdr replaces ntm's tmux backend and pane detection, and nhm's popups
  replace ntm's TUI.
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
| `internal/robot/health.go` | `internal/robot/health.go` (derived) | the session form's types only (`SessionHealthOutput`, `SessionAgentHealth`, `SessionHealthSummary`), without ntm's service panes; the health names as constants; nhm's per-agent extras under `_nhm` |
| `internal/robot/interrupt.go` | `internal/robot/interrupt.go` (derived) | the types only, with the fields nhm fills (no redaction summary; `last_output` stays empty, as nhm reads no screen); the method names as constants; nhm's per-agent results under `_nhm` |
| `internal/robot/wait.go` | `internal/robot/wait.go`, `types.go` (derived) | the types only, with the fields nhm fills (no `wait_id`, `wake_payload`, `cursor_info`, `productivity`, `convergence_streak`); the conditions nhm answers; `AGENT_ERROR` named; nhm's per-agent results under `_nhm` |
| `internal/robot/assign.go` | `internal/robot/assign.go`, `arrays_never_null.go` (derived) | the output types of `--robot-assign` only, with ntm's JSON keys; `pane_id` is the herdr pane id and `pane_target` the slot; `NeverNull`, ntm's arrays-never-null rule for this type (nhm-y55) |
| `internal/robot/assign_test.go` | `internal/robot/assign_test.go` (in part) | provenance header; the JSON tests of the output types; nhm's `NewResponse` for ntm's `NewRobotResponse` (nhm-y55) |
| `internal/robot/spawn.go` | `internal/robot/spawn.go`, `spawn_pacing.go`, `internal/pressure/pressure.go` (derived) | only the fields nhm fills of `SpawnOutput`, `SpawnedAgent`, `SpawnAssignment`, `SpawnStaggerPlan`, `SpawnStaggerSlot` and `SpawnAdmission`; nhm's admission reasons and warnings; extras under `_nhm`; `assignments` always present |
| `internal/core/templates/LICENSE` | `LICENSE` | none |
| `internal/core/templates/template.go` | `internal/templates/template.go` | provenance header; nhm's placeholders (`agent`, `herdr`, `model`, `thread`, `project`, `group`) in `ExecutionContext`; `Template.Enrichment` (plan-v8 2.13) |
| `internal/core/templates/parser.go` | `internal/templates/parser.go` | provenance header; the unused `@macro` pattern removed; nhm's per-target placeholders are pane context and filled in `variables` |
| `internal/core/templates/builtin.go` | `internal/templates/builtin.go` | provenance header |
| `internal/core/templates/parser_test.go` | `internal/templates/parser_test.go` | provenance header; the loader tests use nhm's `NewLoader` and `.nhm/templates` |
| `internal/core/templates/builtin_test.go` | `internal/templates/builtin_test.go` | provenance header |
| `internal/core/snapshot/LICENSE` | `LICENSE` | none |
| `internal/core/snapshot/robot.go` | `internal/robot/robot.go` (derived: `statusOverall`, `statusHealthScore`, the summary counting), `internal/robot/health.go` (derived: `calculateHealthState`'s order and `GetSessionHealth`'s summary switch, in `HealthFor`) | they read nhm's model and its attention severities instead of ntm's alert and state stores; `HealthFor` reads herdr's states and nhm's limit recogniser instead of ntm's screen checks |
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
| `internal/ui/palette/catalog.go` | `internal/config/config.go` (in part) | provenance header; only the `PaletteCmd` and `PaletteState` types, without TOML tags, and `PaletteCmd.Source`; the commands come from nhm's palette catalogs (`templates.ReadCatalog`) in `CatalogPaths`' order, not from ntm's config, without terminal controls |
| `internal/ui/palette/keymap.go` | `internal/palette/keymap.go` | provenance header; import paths. Behaviour: one `TargetKind` binding over nhm's kind digits in place of ntm's per-kind target bindings; `ctrl+o` and `m` show the memory preview; `c` and `s` switch CM and CASS for the sends; nhm's result, variables and Composer keymaps |
| `internal/ui/palette/model.go` | `internal/palette/model.go` | provenance header; import paths; the stand-in types; the title. Behaviour (each listed in the header): panes from nhm's snapshot off the update loop, an older read dropped; `TargetKind` on nhm's kind digits; `Draft` keeps "present" apart from its text, so an emptied draft is refused, never the template; `send` freezes the targets at the keypress and submits once through nhm's send operation, with receipts on a result screen, in place of tmux send-keys and a quit; pins, favourites and recents through `Store`; `intsToStrings` and the history record left out (the core records history); `newEditor`, shared with the Composer; the memory preview and the CM and CASS choice for the sends; a selection another surface hands on is the only target, never widened; text without terminal controls; the target screen's label column fits its longest label, and its rows drop their blank lines and samples when the screen would not fit the popup |
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
| `internal/ui/dashboard/panels/history.go` | `internal/tui/dashboard/panels/history.go` | provenance header; import paths (nhm's `internal/store/history`, the same `HistoryEntry`); boundary edits: `SetPanes` takes `HistoryPane` (key, label, agent type) in place of `tmux.Pane`, so `formatHistoryPaneLabel` goes; `internal/ui/tui/agent`'s agent types; the agent filter reads each entry's `AgentTypes` first, and its third value is Antigravity in place of Gemini; `now` is injectable; `PreviewOpen` is new; behaviour: prompts lose their terminal controls, the empty state and the preview hint name only what the board does, and a row's prompt and targets are cut by display width, never inside a rune |
| `internal/ui/dashboard/panels/history_test.go` | `internal/tui/dashboard/panels/history_test.go` | provenance header; import paths; `HistoryPane` and Antigravity in the agent filter test; three tests are nhm's (the entry's own agent types; clean prompts and an injected now; cuts by display width) |
| `internal/ui/forms/LICENSE` | `LICENSE` | none |
| `internal/ui/forms/spawn_wizard.go` | `internal/tui/dashboard/panels/spawn_wizard.go`, `internal/tui/dashboard/panels/panel.go` (`Keybinding`) | provenance header; package; nhm's theme import path; `Keybinding` copied in as `SpawnKeybinding`. Behaviour: one count (0 to 32) and, where the kind takes one, a model per kind of nhm's registry, instead of ntm's fixed four; the quick and minimal methods keep ntm's mix for the kinds the registry has; new steps after the counts: options (a new session's label, tab label, group, kickoff, memory), the review (the action's dry run: admission and the plan), the confirm and the result; the wizard runs spawn.add or spawn.new through the action table and its guard, where ntm's dashboard ran `ntm add`; going back drops the review; a refused review has no confirm; esc while the spawn runs only closes the wizard (evidence spawn_wizard-closure.md); a method's preset fills the counts once, so going back keeps the operator's edits; view: five steps, with " → " between them |
| `internal/ui/forms/spawn_wizard_test.go` | `internal/tui/dashboard/panels/spawn_wizard_test.go` | provenance header; the constructor takes a runner, a context and nhm's kinds; counts per kind |
| `internal/ui/forms/spawn_wizard_flow_test.go` | `internal/tui/dashboard/panels/spawn_wizard_flow_test.go` | provenance header; nhm's steps (counts, options, review); the tests from `TestSpawnWizardReviewThenSpawnOnce` on are nhm's |
| `internal/core/assignment/LICENSE` | `LICENSE` | none |
| `internal/core/assignment/atomic.go` | `internal/assignment/atomic.go` | provenance header; the events import path; the occupant identity is an nhm binding id (`bnd-` and 16 lowercase hex digits) in place of a tmux pane id (`isPhysicalPaneOccupancyKey`, the `PaneIdentityMigrationError` text, comments; `strconv` dropped); `DispatchRequest.Attempt` carries the persisted attempt (nhm-i85) |
| `internal/core/assignment/store.go` | `internal/assignment/store.go` | provenance header; the events import path; `StorageDir` under `store.StateRoot` in place of `~/.ntm`; nhm names a store `<instance id>.<workspace id>` |
| `internal/core/assignment/store_lock.go` | `internal/assignment/store_lock.go` | provenance header |
| `internal/core/assignment/store_lock_unix.go` | `internal/assignment/store_lock_unix.go` | provenance header; `store_lock_windows.go` is not taken (Linux only) |
| `internal/core/assignment/outcomes.go` | `internal/assignment/outcomes.go` | provenance header; the agent import path (`internal/ui/tui/agent`) |
| `internal/core/assignment/stranded.go` | `internal/assignment/stranded.go` | provenance header |
| `internal/core/assignment/atomic_test.go` | `internal/assignment/atomic_test.go` | provenance header; binding ids in place of `%N`; the identity table keeps each refused shape in binding-id form and adds a tmux pane id, a herdr pane id and a slot |
| `internal/core/assignment/atomic_e2e_test.go` | `internal/assignment/atomic_e2e_test.go` | provenance header; the import path; binding ids in place of `%N` |
| `internal/core/assignment/store_test.go` | `internal/assignment/store_test.go` | provenance header; binding ids in place of `%N`; storage paths under `~/.local/state/nhm` |
| `internal/core/assignment/store_branch_test.go` | `internal/assignment/store_branch_test.go` | provenance header; the storage path under `~/.local/state/nhm` |
| `internal/core/assignment/store_lock_test.go` | `internal/assignment/store_lock_test.go` | provenance header |
| `internal/core/assignment/outcomes_test.go` | `internal/assignment/outcomes_test.go` | provenance header |
| `internal/core/assignment/stranded_test.go` | `internal/assignment/stranded_test.go` | provenance header; a binding id in place of `%993` |
| `internal/core/assignment/events/LICENSE` | `LICENSE` | none |
| `internal/core/assignment/events/events.go` | `internal/events/webhook_event.go`, `internal/events/bus.go` (derived) | the webhook types, `WebhookEvent`, `NewWebhookEvent`, `BusEvent`, `BaseEvent`; `EventEmitter` and `DefaultEmitter` are nhm's stand-in that drops each notice (nhm has no webhook sink) |
| `internal/integrations/worksource/LICENSE` | `LICENSE` | none |
| `internal/integrations/worksource/eligibility.go` | `internal/worksource/eligibility.go` | provenance header |
| `internal/integrations/worksource/identity.go` | `internal/worksource/identity.go` | provenance header |
| `internal/integrations/worksource/snapshot_clock.go` | `internal/worksource/snapshot_clock.go` | provenance header |
| `internal/integrations/worksource/source.go` | `internal/worksource/source.go` | provenance header |
| `internal/integrations/worksource/eligibility_batch_test.go` | `internal/worksource/eligibility_batch_test.go` | provenance header |
| `internal/integrations/worksource/identity_test.go` | `internal/worksource/identity_test.go` | provenance header |
| `internal/integrations/worksource/mutex_claim_test.go` | `internal/worksource/mutex_claim_test.go` | provenance header |
| `internal/integrations/worksource/snapshot_clock_test.go` | `internal/worksource/snapshot_clock_test.go` | provenance header |
| `internal/integrations/worksource/source_test.go` | `internal/worksource/source_test.go` | provenance header |
| `internal/integrations/bv/LICENSE` | `LICENSE` | none |
| `internal/integrations/bv/bv.go` | `internal/bv/bv.go` (in part), `internal/bv/client.go` (`ErrTimeout`) | provenance header; imports; the runners, plan reader, br runner, gates, claim API, br helpers and status/details readers whole; `claimBeadForAssignmentAttempt` claims through br's engine (`br update --claim --if-unchanged`, under nhm's claim lock, ntm's validation and mutex check on one read, a write that never retries, a new read after a refused or failed write) in place of ntm's direct SQLite transaction, which leaves br 0.7.4 refusing writes after a bv read; no automatic JSONL rebuild; the direct SQLite claim/release/stale transactions, `ClaimBead`, `ReleaseBeadClaim` and the bv readers nhm does not call are not taken (nhm-i85); `GetInsightsContext` whole, for the human `spawn --assign`'s cycle check (nhm-cvw) |
| `internal/integrations/bv/assignment_mutex.go` | `internal/bv/assignment_mutex.go` (derived) | provenance header; the error types whole; `requireAssignmentMutexes` keeps ntm's rules on the claim's read and `br list --all` in place of SQL in the transaction |
| `internal/integrations/bv/timeout.go` | `internal/bv/timeout.go` | provenance header |
| `internal/integrations/bv/triage.go` | `internal/bv/triage.go` | provenance header; util and worksource import paths |
| `internal/integrations/bv/types.go` | `internal/bv/types.go` | provenance header |
| `internal/integrations/bv/work_source.go` | `internal/bv/work_source.go` | provenance header; the worksource import path |
| `internal/integrations/bv/bead_status_test.go` | `internal/bv/bead_status_test.go` (in part) | provenance header; imports; two transaction tests call the public claim; the stale, non-terminal, release, rebuild and hash tests are not taken |
| `internal/integrations/bv/lists.go` | `internal/bv/bv.go` (in part: `GetInProgressListContext`, `GetBlockedListContext`) | provenance header; `GetBlockedListContext`'s doc comment without `HasLocalBeadsDB`, which nhm does not take (nhm-y55) |
| `internal/integrations/bv/lists_test.go` | `internal/bv/bv_helpers_test.go` (in part: `TestGetInProgressListContextZeroLimitReturnsFullList`) | provenance header; a helper for the test project; plus nhm's tests of the limits and of `GetBlockedListContext` (nhm-y55) |
| `internal/integrations/bv/bv_helpers_test.go` | `internal/bv/bv_helpers_test.go` (in part) | provenance header; the context test keeps its two assignment-claim cases; tests of readers not taken are left out |
| `internal/integrations/bv/bv_pure_helpers_test.go` | `internal/bv/bv_pure_helpers_test.go` (in part) | provenance header; the markdown and client tests are left out |
| `internal/integrations/bv/bv_test.go` | `internal/bv/bv_test.go` (in part) | provenance header; imports; tests of readers not taken are left out |
| `internal/integrations/bv/timeout_test.go` | `internal/bv/timeout_test.go` (in part) | provenance header; imports; the two GetInsights tests are left out |
| `internal/integrations/bv/triage_accessors_test.go` | `internal/bv/triage_accessors_test.go` (in part) | provenance header; `TestTriageAccessors_CachePrimed` is left out |
| `internal/integrations/bv/triage_source_test.go` | `internal/bv/triage_source_test.go` | provenance header |
| `internal/integrations/bv/triage_test.go` | `internal/bv/triage_test.go` | provenance header |
| `internal/integrations/bv/work_source_test.go` | `internal/bv/work_source_test.go` | provenance header; the worksource import path |
| `internal/integrations/bv/work_source_batch_test.go` | `internal/bv/work_source_batch_test.go` | provenance header |
| `internal/integrations/bv/util/LICENSE` | `LICENSE` | none |
| `internal/integrations/bv/util/dirs.go` | `internal/util/dirs.go` (in part) | provenance header; package comment; only `ResolveProjectDir` and what it calls; `findGitRootUncached` sets `cmd.WaitDelay` (nhm-fty) |
| `internal/core/spawnassign/LICENSE` | `LICENSE` | none |
| `internal/core/spawnassign/spawn_ntm.go` | `internal/robot/spawn.go` (in part) | provenance header; the assignment loop and its helpers at the backend boundary: `Policy` for `config.Config`, verified occupants (binding id, generation, slot) for tmux panes, `ObserveOccupant` for tmux observation, the `Dispatch` port (nhm's send core) for the tmux dispatch service, the Mail name as claim name, no reservation branch, the slot as coverage key; `enrichSpawnWorkPrompt` prepares the context through nhm's enrichment for the occupant and the intent's key (nhm-i85); an optional per-occupant `Preamble` ahead of each work prompt, and an optional `SelectWork` that picks the work in place of the strategies, each item with its own work prompt if it has one (`workItem.prompt`) (nhm-cvw) |
| `internal/core/spawnassign/human_ntm.go` | `internal/cli/assign.go`, `internal/cli/spawn.go` (`resolveSpawnAssignAgentType`), `internal/robot/robot.go` (`ResolveAgentType`) (in part) | provenance header; package and imports; the two resolvers exported for the CLI; `printAssignmentPromptContext` returns its lines (`HumanPromptContextLines`); the human `spawn --assign` selection (nhm-cvw): `classifyTriageRecForAssignmentWithGate`, `normalizeBeadStatus`, `partitionActionableRecommendationsForAssignment`, `CheckCycles`' validation, `IsBeadInCycle`, `getAssignOutputEnhanced`'s bead preparation and limit, `buildAssignAllocationInput`, `buildAssignAllocationBeads`, `assignBeadResourceCost`, `assignAgentResourceHeadroom`, `assignScrollbackBacklog`, `clampAssignScore`, `assignmentItemsFromAllocationPlan`, and `promptTemplateSource`'s default through `renderPromptTemplate`, on the verified plan and spawnassign's ready agents (slot for pane target, key and index); cycles, active beads and load from the caller; context use and scrollback count as none, and the system pressure probe gives way to its reading at normal load; one item per agent for `SelectWork`, each with ntm's work prompt, the rest failed unclaimed with the words of ntm's observation gate; with no agent, every ready bead skipped as `no_placeable_agents` |
| `internal/core/spawnassign/prepared.go` | `internal/robot/prompt_context.go` (`AssignmentPrompt.retrievalQuery`, in part) | provenance header; nhm's redaction; the rest of the file is nhm's |
| `internal/core/spawnassign/bulk_assign_ntm.go` | `internal/robot/bulk_assign.go` (in part) | provenance header; the replay helpers take the occupancy key beside the dispatch target |
| `internal/core/spawnassign/assign_ntm.go` | `internal/robot/assign.go` (in part), `internal/robot/bulk_assign.go` (`assignmentDependencyMissing`) | provenance header; `--robot-assign`'s code (nhm-y55): `AssignOptions.Ledger`, the instance id that names the ledger; `GetAssign` on nhm's ports (`RobotAssignDependencies`), with the session's check left to the caller; nhm's `observeAssignAgents` and `excludeDurablyOccupiedAssignAgents` (`robot_assign.go`), so `assignAgentInfo` gains the binding id; nhm's `newRobotResponse` and `newErrorResponse`; nhm in the session hint; no suggested command, as nhm has no `--robot-bulk-assign`; the output types are `internal/robot`'s. Not taken: `PrintAssign`, the distribute mode, `getAssignableActionableRecommendations`, `assignAgentsFromObservation`, `loadAuthoritativeAssignmentPolicy` |
| `internal/core/spawnassign/assign_ntm_test.go` | `internal/robot/assign_test.go`, `assign_strategy_wire_test.go` (in part) | provenance header; binding ids where the ledger reads them; `GetAssign`'s ports; no suggested command expected; the `DistributeRecommendation` lines left out; the tmux observation, config, `PrintAssign` and distribute tests not taken (nhm-y55) |
| `internal/core/spawnassign/policy.go` | `internal/config/merge.go`, `project.go`, `config.go` (derived) | the `[assign]` section only: nhm's config as the global file, the project's `.ntm/config.toml` merged by ntm's rules; an unknown key under `[assign]` refuses; `mergeOperatorGatedLabels` whole |
| `internal/core/spawnassign/spawn_ntm_test.go` | `internal/robot/spawn_test.go` (in part) | provenance header; the strategy, prompt, finalize and schema tests; the tmux "single-window pane" finalize case left out |
| `internal/core/spawnassign/assign/LICENSE` | `LICENSE` | none |
| `internal/core/spawnassign/assign/capability.go` | `internal/assign/capability.go` | provenance header; the agent import path; `tmux.AgentType` and its constants, aliases of `agent.AgentType`'s, written as the latter |
| `internal/core/spawnassign/assign/allocation.go` | `internal/assign/allocation.go` | provenance header; the agent import path; `tmux.AgentType`, an alias of `agent.AgentType`, written as the latter |
| `internal/core/spawnassign/assign/allocation_batch.go` | `internal/assign/allocation_batch.go` | provenance header |
| `internal/core/spawnassign/assign/allocation_test.go` | `internal/assign/allocation_test.go` | provenance header; the agent import path; `tmux.AgentType` and its constants written as `agent.AgentType`'s |
| `internal/core/spawnassign/assign/allocation_plan_batch_test.go` | `internal/assign/allocation_plan_batch_test.go` | provenance header |
| `internal/core/spawnassign/assign/allocation_batch_test.go` | `internal/assign/allocation_batch_test.go` | provenance header |
| `internal/core/spawnassign/assign/capability_test.go` | `internal/assign/capability_test.go` | provenance header; the agent import path; `tmux.AgentType` and its constants written as `agent.AgentType`'s |
| `internal/core/spawnassign/assign/matcher.go` | `internal/assign/matcher.go` | provenance header; the agent import path; `tmux.AgentType`, an alias of `agent.AgentType`, written as the latter (nhm-y55) |
| `internal/core/spawnassign/assign/priority_batch.go` | `internal/assign/priority_batch.go` | provenance header |
| `internal/core/spawnassign/assign/matcher_test.go` | `internal/assign/matcher_test.go` | provenance header; the agent import path; `tmux.AgentType` and its constants written as `agent.AgentType`'s |
| `internal/core/spawnassign/assign/matcher_balanced_eligibility_test.go` | `internal/assign/matcher_balanced_eligibility_test.go` | provenance header; the agent import path; `tmux.Agent*` constants written as `agent.AgentType*` |
| `internal/core/spawnassign/assign/matcher_priority_batch_test.go` | `internal/assign/matcher_priority_batch_test.go` | provenance header; the agent import path; `tmux.Agent*` constants written as `agent.AgentType*`; one local `agent` is `worker`, since `agent` names the package |
| `internal/core/spawnassign/assign/priority_batch_test.go` | `internal/assign/priority_batch_test.go` | provenance header |
| `internal/core/spawnassign/assign/legacy_ntm.go` | `internal/cli/assign.go`, `internal/cli/activity.go` (in part) | provenance header; the selection code of ntm's human assign as pure functions: `generateAssignmentsLegacy` is `LegacyAssignments`, on `LegacyAgent` (the pane fields it reads), with the store's load passed in as `LegacyLoad` and a clock called for each pick; `usesAllocationPlanner` is `UsesAllocationPlanner`, on the strategy string; `detectAgentTypeFromPane` is `LegacyAgentType`, on the agent type, without the title fallback; `parsePriorityString` and `inferTaskTypeFromBead` exported; `calculateMatchConfidence` keeps its task patterns in a fixed order where ntm ranges over a map (one of ntm's answers); the caller fills the agent name, pane id, status, prompt flag and time, and prints the round-robin `--verbose` plan |
| `internal/core/spawnassign/assign/legacy_ntm_test.go` | `internal/cli/assign_test.go`, `coverage_boost_test.go`, `cli_helpers_test.go`, `cli_helpers_extra_test.go`, `activity_test.go`, `omp_test.go`, `helpers_batch3_test.go` (in part) | provenance header; only the tests of the functions in `legacy_ntm.go`, on `LegacyAgent`, `LegacyAssignments` and `LegacyAgentType`; the caller's fields (name, status, prompt flag, time) not checked; a note on nhm's task type in one test; plus nhm's tests of the local changes |
| `internal/core/panediff/LICENSE` | `LICENSE` | none |
| `internal/core/panediff/diff_ntm.go` | `internal/output/diff.go` | provenance header; the package; `ComputeDiff`'s and `DiffResult`'s comments |
| `internal/core/panediff/diff_ntm_test.go` | `internal/output/diff_errors_test.go` (in part) | provenance header; the package; only the tests of `diff.go` (`ComputeDiff`, `countLines`) |
| `internal/core/panediff/codeblock_ntm.go` | `internal/codeblock/parser.go` (in part) | provenance header; the package; only what `ntm diff --code-only` uses: `CodeBlock`'s language, content and lines, the language names, and the fence parser as `ExtractFromText` itself (ntm's `Parser.Parse`, without the `Parser` type and its language filter), with its comments on the fences shortened; the file path detection, `Extraction` and `DetectLanguage` left out |
| `internal/core/panediff/codeblock_ntm_test.go` | `internal/codeblock/parser_test.go` (in part) | provenance header; the package; only the tests of what `codeblock_ntm.go` keeps |
| `internal/cli/LICENSE` | `LICENSE` | none |
| `internal/cli/watch.go` | `internal/cli/watch.go` (in part) | provenance header; ntm's `watch` on herdr's agents (nhm-zac): the flags, the stream, `--bead` and `--pattern`/`--command` modes, their texts and NDJSON frames; each agent named after its mail name, herdr name or pane id; ntm's `--no-color` text (no colours), times in UTC; an agent's new lines by logs -f's live region and `freshLines`, not `computeDiff`; `--pane` takes a name, slot or pane id; without a session, the calling pane's workspace; the project of the session's bound agents; ntm's fsnotify watcher (`internal/core/watcher`), whose debounce only a matching change holds back, and the bursts that come during a send go as one (nhm-98l); when fsnotify cannot start or inotify has no watch left, a paced polling scan, whose matching change waits for a scan without one, said once on stderr (ntm polls on its watcher's ticker only when fsnotify cannot start); `--command` through core send to the bound agents, the calling pane left out; nhm's extras: agents named after the session, `--count` |
| `internal/cli/watch_test.go` | `internal/cli/watch_test.go`, `internal/cli/watch_json_truth_test.go` (in part) | provenance header; only the tests of the functions in `watch.go`, on nhm's agents and `watchOut`; plus nhm's tests of the verb on a herdr test server |
| `internal/cli/flagerrors_ntm.go` | `internal/cli/root.go` (in part) | provenance header; ntm's flag error function (`flagError`, which `newRoot` sets on the root), `unknownFlagFromError`, `flagMisGuessHints`, `curatedFlagHint`, `flagErrorGuidance`, `nearestFlagName`, `commonPrefixLen` and `levenshteinDistance`; `classifyRobotExecuteError` is `rootErrorCode`, given the root's flags, without ntm's `--robot-status`, CLI-input and cancellation cases; the hints name nhm (nhm-2p6.1.1) |
| `internal/cli/flagerrors_ntm_test.go` | `internal/cli/root_flag_hints_test.go` | provenance header |
| `internal/core/session/LICENSE` | `LICENSE` | none |
| `internal/core/session/resolve.go` | `internal/session/resolve.go`, `internal/cli/util.go` (`SessionResolution`, `ExplainIfInferredForOutput`, `ResolveSessionWithOptionsContext`, `inferSessionFromCWD`, `orderSessionsForSelection`), `internal/tmux/session.go` (`ValidateSessionName`), `internal/config/label.go` (`HasLabel`) (in part) | provenance header; a session is a herdr workspace, named by its label (or its id without one); an exact label or workspace id comes before ntm's name check, and a label that two workspaces share matches neither; the current session is the calling pane's workspace; a session's project directories are its bound agents' project roots, not `projects_base/<session>`; the session list, the cwd, the terminal check and the picker are the caller's; "tmux" and "ntm" in the texts read "herdr" and "nhm" (nhm-2p6.1.2) |
| `internal/core/session/resolve_test.go` | `internal/session/resolve_test.go`, `internal/cli/util_test.go` (`TestOrderSessionsForSelection`, `TestInferSessionFromCWD_LabelDisambiguation`) (in part) | provenance header; nhm's `Session` values; the cwd and the project directories are arguments, so the remote case is gone; plus nhm's tests of `Resolve` |
| `internal/core/watcher/LICENSE` | `LICENSE` | none |
| `internal/core/watcher/debouncer.go` | `internal/watcher/debouncer.go` | provenance header |
| `internal/core/watcher/debouncer_test.go` | `internal/watcher/debouncer_test.go`, `internal/watcher/helpers_test.go` (in part) | provenance header; `Duration` from the helpers |
| `internal/core/watcher/watcher.go` | `internal/watcher/watcher.go` (in part) | provenance header; only what `ntm watch --pattern` uses, on fsnotify (nhm-98l): the polling mode and `WithPollInterval` left out, so `New` returns fsnotify's error and nhm watch polls on its own; a recursive add stops at the first folder that inotify has no watch left for and returns `ErrNoWatches` (ntm reports each folder and goes on); `WithErrorHandler` (a test helper in ntm) is an option; `WithMatch` is new: an event whose path does not match does not reach the handler or hold back the debounce; `WithEventFilter`, `WithDebounceDuration` and `WithDebouncer` left out; a folder's add through a field, for tests |
| `internal/core/watcher/watcher_test.go` | `internal/watcher/watcher_test.go`, `internal/watcher/helpers_test.go` (in part) | provenance header; only the tests of the fsnotify watcher that `watcher.go` keeps; `WatchedPaths` and ntm's `WithDebouncer` as test helpers; plus nhm's tests of `WithMatch`, a new folder's watch and running out of watches |
| `internal/integrations/cm/LICENSE` | `LICENSE` | none |
| `internal/integrations/cm/daemon.go` | `internal/cm/client.go` (in part), `internal/cli/memory.go` (`findSessionID`) | provenance header; only the daemon client that `memory context` and `outcome` use (`NewClient`, `NewPortClient`, `rpc`, `callTool`, `GetContext`, `Health`, `RecordOutcome`, the context types and `PIDFileInfo`); the pid files under `.nhm/pids` (the operator's ruling, 2026-10-24); the daemon's rule and snippet types named `DaemonRule` and `DaemonSnippet`; `RecordOutcome` takes the status and rules, without ntm's sentiment and notes, which `memory outcome` never sets |
| `internal/integrations/cm/serve.go` | `internal/supervisor/supervisor.go`, `health.go`, `ownership.go`, `ownership_unix.go`, `procattr_unix.go` (in part) | provenance header; cm only, one daemon per `Server`, one goroutine owns each generation where ntm's locks share it; the cm entry of `DefaultSpecs` as `ServeConfig`'s defaults; the MCP probe only; the pid, log and fence files under `.nhm/` |
| `internal/cli/memory_ntm.go` | `internal/cli/memory.go` (in part) | provenance header; ntm's memory verb on nhm's cobra root, over `internal/integrations/cm`; `.nhm/` paths in its texts and errors; the texts name nhm; output to cobra's writers; the cm command and the serve context replaceable by tests |
| `internal/core/spawn/LICENSE` | `LICENSE` | none |
| `internal/core/spawn/agent_spec_ntm.go` | `internal/cli/agent_spec.go` (in part) | provenance header; package and imports; a `Want` of an nhm kind for ntm's `AgentSpec` of an `AgentType`; the kind's adapter says whether it takes the `@effort` suffix (an effort flag) and whether its model is pinned (`PinnedModel`), for ntm's type switches; the pinned model's refusal names the kind's ntm type and flag (nhm-2p6.4.1) |
| `internal/core/spawn/agent_spec_ntm_test.go` | `internal/cli/agent_spec_test.go` (in part) | provenance header; package and imports; the spec cases through `ParseCount` with an nhm kind, one table per test (nhm-2p6.4.1) |

## Files from herdr

| nhm path | Source | Local changes |
|---|---|---|
| `internal/herdr/schema/herdr-api.schema.json` | the output of `herdr api schema --json`, herdr 0.9.3, protocol 22 (github.com/herdrdev/herdr, Apache-2.0) | none |
