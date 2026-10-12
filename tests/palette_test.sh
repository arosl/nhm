#!/usr/bin/env bash
# Test scripts/nhm-palette without a terminal: the palette files, the
# targets from nhm's read model, and the `nhm send` calls a selection
# makes. A fake nhm stands in for the real one.
#
# Usage: tests/palette_test.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT

fails=0
pass() { printf 'PASS %s\n' "$1"; }
fail() {
  printf 'FAIL %s\n' "$1"
  fails=$((fails + 1))
}

export HOME="$work/home" XDG_CONFIG_HOME="$work/home/.config" XDG_STATE_HOME="$work/home/.local/state"
export ACFS_HOME="$work/acfs" NHM_CONTEXT=""
unset NHM_PALETTE_FILE NHM_PALETTE_CWD HERDR_WORKSPACE_ID
mkdir -p "$HOME/.config/nhm" "$ACFS_HOME/onboard/docs/ntm" "$work/proj" "$work/bin"

cat > "$ACFS_HOME/onboard/docs/ntm/command_palette.md" << 'EOF'
## Review
### fresh_review | Fresh review
Review your change.

### shared | ACFS's shared
from ACFS
EOF
cat > "$HOME/.config/nhm/command_palette.md" << 'EOF'
## Mine
### shared | My shared
from the user
EOF
cat > "$work/proj/command_palette.md" << 'EOF'
## Project
### threaded | Threaded
Reply on {{thread}}.
EOF

# The fake nhm: status prints two workspaces' agents; send records its
# arguments.
cat > "$work/bin/nhm" << EOF
#!/usr/bin/env bash
case "\$1" in
  status)
    cat << 'JSON'
{"schema":"nhm.v1","command":"status","result":{"agents":[
 {"pane_id":"p1","workspace_id":"w1","slot":"1.1","kind":"claude","state":"idle","binding":{"mail_name":"IcyKnoll"}},
 {"pane_id":"p2","workspace_id":"w1","slot":"1.2","kind":"codex","state":"working","binding":{"mail_name":"RedFox"}},
 {"pane_id":"p3","workspace_id":"w1","slot":"1.3","kind":"claude","state":"idle"},
 {"pane_id":"p4","workspace_id":"w2","slot":"2.1","kind":"claude","state":"done","binding":{"mail_name":"BlueLake"}}
]},"warnings":[],"error":null}
JSON
    ;;
  send) printf '%s\n' "\$*" >> "$work/sends" ;;
esac
EOF
chmod +x "$work/bin/nhm"
export NHM_BIN="$work/bin/nhm"

palette="$root/scripts/nhm-palette"
cd "$work/proj"

list="$(bash "$palette" --list)"
if [[ "$(cut -f1,2 <<< "$list")" == $'threaded\tThreaded\nshared\tMy shared\nfresh_review\tFresh review' ]]; then
  pass "palette files: the project's, the user's, then ACFS's; first key wins"
else
  fail "palette files: $list"
fi
if [[ "$(bash "$palette" --show shared)" == "from the user" ]]; then
  pass "--show prints the winning prompt's text"
else
  fail "--show: $(bash "$palette" --show shared 2>&1)"
fi
if [[ "$(bash "$palette" --list --json | jq -r '.[2].category')" == "Review" ]]; then
  pass "--list --json"
else
  fail "--list --json"
fi
if ! bash "$palette" < /dev/null > /dev/null 2>&1; then
  pass "the interactive palette needs a terminal"
else
  fail "the interactive palette needs a terminal"
fi

# shellcheck source=/dev/null
source "$palette"
set +e

rows="$(palette_target_rows w1)"
if [[ "$(grep -c '^group' <<< "$rows")" == 3 && "$(grep -c '^agent' <<< "$rows")" == 3 ]] &&
  grep -q $'^agent\tp3\t-\tw1\t' <<< "$rows" && grep -q $'^agent\tp1\tIcyKnoll\tw1\t' <<< "$rows"; then
  pass "targets: all, then one group per kind, then the workspace's agents"
else
  fail "targets: $rows"
fi

calls="$(palette_send_calls w1 "$(grep $'^group\tall' <<< "$rows")")"
if [[ "$calls" == $'w1\t--all' ]]; then pass "one group 'all' is --all"; else fail "group all: $calls"; fi
calls="$(palette_send_calls w1 "$(grep $'^group\tkind:codex' <<< "$rows")")"
if [[ "$calls" == $'w1\t--type\tcodex' ]]; then pass "one kind group is --type"; else fail "kind group: $calls"; fi
calls="$(palette_send_calls w1 "$(grep -E $'^agent\tp(1|3)\t' <<< "$rows")" 2> "$work/note")"
if [[ "$calls" == $'w1\t--name\tIcyKnoll' ]] && grep -q 'skipped pane p3' "$work/note"; then
  pass "single agents are --name; an unbound one is skipped with a note"
else
  fail "single agents: $calls / $(cat "$work/note")"
fi
calls="$(palette_send_calls w1 "$(grep -E $'^(group\tkind:codex|agent\tp1)\t' <<< "$rows")")"
if [[ "$calls" == $'w1\t--name\tIcyKnoll\t--name\tRedFox' ]]; then
  pass "a group and an agent are expanded to names"
else
  fail "group and agent: $calls"
fi
allrows="$(palette_target_rows "")"
calls="$(palette_send_calls "" "$(grep $'^group\tkind:claude' <<< "$allrows")")"
if [[ "$calls" == $'w1\t--name\tIcyKnoll\nw2\t--name\tBlueLake' ]]; then
  pass "outside a workspace, a group is one send per workspace, by name"
else
  fail "every workspace: $calls"
fi
if ! palette_send_calls w1 "$(grep $'^agent\tp3' <<< "$rows")" > /dev/null 2>&1; then
  pass "only unbound agents: nothing to send"
else
  fail "only unbound agents"
fi

printf 'Reply on {{thread}}.\n' > "$work/prompt.md"
palette_send "$work/prompt.md" w1 "$(grep $'^group\tall' <<< "$rows")" <<< "acfs-7" 2> /dev/null
if [[ "$(cat "$work/sends")" == "send w1 --all -t $work/prompt.md --var thread=acfs-7" ]]; then
  pass "the send is nhm send <ws> … -t FILE.md, with the thread as --var"
else
  fail "send: $(cat "$work/sends")"
fi

palette_toggle pins fresh_review
if palette_state_has pins fresh_review && palette_state_has favorites fresh_review &&
  [[ -f "$XDG_STATE_HOME/nhm/palette/pins" ]] && [[ "$(palette_rows | head -n 1 | cut -f1)" == fresh_review ]]; then
  pass "a pin is a favorite too, comes first, and lives under ~/.local/state/nhm/palette"
else
  fail "pins"
fi
palette_toggle favorites fresh_review
if ! palette_state_has pins fresh_review && ! palette_state_has favorites fresh_review; then
  pass "unfavoriting a pinned prompt unpins it"
else
  fail "unfavorite"
fi

NHM_CONTEXT='{"socket_path":"/s","workspace_id":"w9","cwd":"'"$work/proj"'"}'
if [[ "$(palette_context workspace_id)" == w9 ]] && (cd / && NHM_PALETTE_CWD="" palette_cwd) | grep -qx "$work/proj"; then
  pass "the captured context gives the workspace and the directory"
else
  fail "context"
fi

if [[ $fails -eq 0 ]]; then
  echo "RESULT: pass"
else
  echo "RESULT: $fails failure(s)"
  exit 1
fi
