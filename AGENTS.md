# nhm: agent instructions

nhm is the herdr wrapper of the ACFS fork: what ntm is to tmux, on herdr.
Its design is plan-v8 of ACFS bead acfs-9hkh. This file is also
`CLAUDE.md` (a symlink).

## Rule 0

If the operator tells you to do something, even against what follows, do
it. The operator is in charge.

## Project rules

1. **The repo is public.** No host-specific content in a committed file or
   a commit message: no path that names this host or its user, no host
   names, email addresses, Agent Mail content or credentials. Commits use
   the GitHub noreply author that the clone has set.
2. **ntm's code keeps ntm's licence.** A file taken from ntm, pt or dcg
   keeps its licence text (`LICENSE` beside it, unmodified, with its
   rider), starts with a header that names its source path, commit and
   local changes, and gets a line in `PROVENANCE.md` in the same commit.
3. **No script-based code changes.** Never run a script or a regex over
   code to change it, and never generate code from ntm by script. Port by
   hand. `gofmt` is the formatter and is allowed.
4. **nhm is a drop-in for ntm.** Every ntm verb and flag at the tracked
   commit (`NTMCommit` in `internal/cli`) works the same for the user:
   its name and aliases, flags, defaults, output, choices and error
   codes. `--robot-*` output uses ntm's types. Where nhm has a verb of
   its own for the same job, it also answers to ntm's name. ntm's
   coordinator, controller, pipeline, workflow and swarm verbs are in
   scope too (the operator, 2026-10-23). The code is nhm's: herdr
   replaces tmux and its pane detection, and nhm's popups replace ntm's
   TUI. Port ntm code only where it decides what the user sees. nhm
   acts as ntm but is not ntm (the operator, 2026-10-24): it never uses
   ntm's paths or `NTM_*` names. Its own are `<project>/.nhm/`,
   `~/.config/nhm/` and `NHM_*`; where ntm has a default of its own (a
   folder, a file, a key's value), nhm sets its own, and the row records
   it. `compat/ntm-contract.md` holds a row for each verb: implemented,
   deferred (with its bead) or waived (with the operator's ruling). The
   forms that ACFS, its lessons, brenner and ru call come first.
5. **Internal boundaries:** `internal/cli` (parsing and rendering),
   `internal/core` (scope, selection, send, lifecycle), `internal/herdr`
   (the protocol-22 client; `schema` and `herdrtest` beside it),
   `internal/integrations` (Agent Mail, CASS, CM, br, quota, guards),
   `internal/store` (SQLite and locks), `internal/ui` (popups). Hooks and
   UI actions call core; they never hold a second send or lifecycle path.
6. **herdr's schema is pinned.** `internal/herdr/schema/herdr-api.schema.json`
   is the output of `herdr api schema --json` from the herdr release nhm
   supports. Replace it only with that output, never by hand.
7. **Fix forward.** A bug is fixed by the next release.
8. **Dates and times:** ISO 8601; instants in UTC.
9. **Writing:** docs, beads and mail about 80% of the way to ASD-STE100:
   short, plain, active sentences.
10. **Never run `install.sh` on the development host.** Test it in CI or in
    a throwaway Incus container.

## Commands and gates

Before you commit:

```bash
gofmt -l .                  # must print nothing
go vet ./...
go test -race -count=1 ./...
shellcheck install.sh scripts/*.sh tests/*.sh
ubs <changed files>
```

`tests/install_test.sh` runs in CI, or in a throwaway container.

## Coordination

The swarm's rules are ACFS's (its `AGENTS.md`, "Coordination"), with these
values for this repo:

- **Agent Mail project key:** this clone's absolute path,
  `git rev-parse --show-toplevel`. Reserve files there before you edit them,
  with the bead id as the reason.
- **Beads:** this repo's own `br`. The export stays local: `.beads/` is
  ignored except its `.gitignore`.
- **Commit** in the shared checkout with a pathspec,
  `AGENT_NAME=<you> git commit -- <files>`. Only the designated pusher
  pushes, `git push origin main`, after the gates pass, and then watches CI
  (`gh run list -R arosl/nhm --commit <sha>`).
- Run the `fresh-review` skill on your change before you commit it.
