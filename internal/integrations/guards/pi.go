package guards

// nhm's pi guard (nhm-dyr). pi (pi_agent_rust 0.8.0) runs an extension's
// tool_call hook before each tool call, and the hook can block the call.
// nhm writes the extension below into the instance dir and pi loads it
// with -e at launch, as Claude gets the dcg hook through --settings. It
// sends every bash command to dcg's robot test and blocks the call unless
// dcg allows it. It blocks pi's hub, debug and subagent tools, which start
// programs that dcg cannot screen, and every tool that xdev runs but bash
// and pi's tools that start no program.
//
// What the sandbox runs against ACFS's mock API proved (pi 0.8.0):
//   - a hook answers {block, reason}; pi ignores a changed or returned
//     input, so a hook cannot route a build through rch;
//   - pi.exec(cmd, args, {timeout}) gives {code, killed, stdout, stderr}
//     and takes no stdin, so the command goes on dcg's argv after "--";
//   - exec needs the "exec" capability: --extension-policy permissive
//     grants it, balanced asks first (also with allowDangerous) and safe
//     denies it;
//   - pi quarantines a hook that runs 5 s. With
//     PI_EXTENSION_HOOKS_FAIL_CLOSED=1 (which wins over pi's settings) it
//     then blocks that call and every later one, as it blocks the call of
//     a hook that throws; without it, pi runs the call;
//   - a missing, unparsable or throwing extension stops pi at startup;
//   - "xdev run hub" starts a program, and the hook sees only the xdev call.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// PiExtensionDir is the directory below the instance dir that holds nhm's
// pi guard extension.
const PiExtensionDir = "pi"

// PiPolicy is the --extension-policy a guarded pi launches with: the one
// profile that lets the extension run dcg without a prompt that nobody
// answers in an agent's pane.
const PiPolicy = "permissive"

// piExtensionTemplate is the extension; __DCG__ becomes dcg's absolute
// path as a JSON string. It is plain JavaScript (pi reads it as
// TypeScript), so the tests run it under node. DCG_TIMEOUT_MS stays under
// pi's 5 s hook limit. It refuses to load when pi's hooks do not fail
// closed (a pane that nhm did not create), so that pi does not start: a
// quarantined hook would otherwise let every later call run unscreened.
// xdev runs a tool unseen by the hook, so xdev passes only its read-only
// actions, bash (screened) and pi's tools that start no program.
const piExtensionTemplate = `// nhm's pi guard (nhm-dyr). nhm writes this file at each launch and pi
// loads it with -e. A bash command runs only when dcg allows it.
const DCG = __DCG__;
const DCG_TIMEOUT_MS = 3000;
const UNSCREENED = ["hub", "debug", "subagent"];
const XDEV_ACTIONS = ["list", "describe", "promote"];
const XDEV_RUN = ["ast_grep", "ast_edit", "lsp", "jobs", "manage_skill"];
const FAIL_CLOSED = ["1", "true", "yes"];

function name(v) {
  return String(v ?? "").trim().toLowerCase();
}

function block(reason) {
  return { block: true, reason: "nhm's pi guard: " + reason };
}

async function screen(pi, command) {
  if (typeof command !== "string") {
    return block("the bash call has no command string");
  }
  const r = await pi.exec(DCG, ["test", "--robot", "--dialect", "posix", "--", command], { timeout: DCG_TIMEOUT_MS });
  let v = undefined;
  try {
    v = JSON.parse(String(r?.stdout ?? ""));
  } catch (e) {
    v = undefined;
  }
  if (r?.code === 0 && r?.killed !== true && v?.decision === "allow") {
    return undefined;
  }
  if (r?.code === 1 && v?.decision === "deny") {
    return block("dcg denies it: " + String(v?.reason ?? "") + (v?.rule_id ? " [" + String(v.rule_id) + "]" : ""));
  }
  return block("dcg gave no allow (exit " + String(r?.code) + (r?.killed ? ", killed at the limit" : "") + "), so the command does not run");
}

function args(input) {
  return typeof input === "string" ? JSON.parse(input) : input;
}

export default function (pi) {
  if (!FAIL_CLOSED.includes(name(process.env.PI_EXTENSION_HOOKS_FAIL_CLOSED))) {
    throw new Error("nhm's pi guard needs PI_EXTENSION_HOOKS_FAIL_CLOSED=1, which nhm sets in the panes it creates: start pi with nhm spawn");
  }
  pi.on("tool_call", async (event) => {
    try {
      let tool = name(event?.toolName);
      let input = event?.input;
      if (tool === "xdev") {
        input = args(input);
        const action = name(input?.action);
        if (XDEV_ACTIONS.includes(action)) {
          return undefined;
        }
        if (action !== "run") {
          return block("xdev " + action + " is no call that nhm's pi guard knows");
        }
        tool = name(input?.name);
        if (tool === "bash") {
          input = args(input?.args);
        } else if (!XDEV_RUN.includes(tool)) {
          return block("xdev runs " + tool + " unseen by the guard, and pi's " + tool + " tool may start programs that dcg cannot screen; run the command with bash");
        }
      }
      if (tool === "bash") {
        return await screen(pi, input?.command);
      }
      if (UNSCREENED.includes(tool)) {
        return block("pi's " + tool + " tool starts programs that dcg cannot screen; run the command with bash");
      }
      return undefined;
    } catch (e) {
      return block(String(e));
    }
  });
}
`

// PiExtension is the source of nhm's pi guard extension for a dcg binary.
func PiExtension(dcg string) []byte {
	lit, _ := json.Marshal(dcg)
	return []byte(strings.Replace(piExtensionTemplate, "__DCG__", string(lit), 1))
}

// WritePiExtension writes nhm's pi guard extension for an absolute dcg
// path into dir and returns the file's path. The name holds a hash of the
// content, so a launch never reads another launch's file half written;
// a file that someone changed is written again.
func WritePiExtension(dir, dcg string) (string, error) {
	if !filepath.IsAbs(dcg) {
		return "", fmt.Errorf("guards: dcg path %q is not absolute", dcg)
	}
	src := PiExtension(dcg)
	sum := sha256.Sum256(src)
	path := filepath.Join(dir, "nhm-guard-"+hex.EncodeToString(sum[:6])+".ts")
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, src) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".nhm-guard-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(src)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return path, nil
}

// piDCGLine is the line of the extension that names dcg.
var piDCGLine = regexp.MustCompile(`(?m)^const DCG = ("[^\n]*");$`)

// piExtensionLimit bounds what nhm reads of a launch's extension file.
const piExtensionLimit = 64 << 10

// isPiGuard reports whether a file is nhm's pi guard extension: exactly
// the source PiExtension gives for an absolute dcg path.
func isPiGuard(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, piExtensionLimit+1))
	if err != nil || len(data) > piExtensionLimit {
		return false
	}
	m := piDCGLine.FindSubmatch(data)
	var dcg string
	if m == nil || json.Unmarshal(m[1], &dcg) != nil {
		return false
	}
	return filepath.IsAbs(dcg) && bytes.Equal(data, PiExtension(dcg))
}

// piKeeps applies pi's flags to a launch argv: the launch keeps nhm's
// guard when -e loads nhm's extension and the last --extension-policy is
// PiPolicy.
func piKeeps(argv []string) (bool, string) {
	loaded, policy := false, ""
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case (a == "-e" || a == "--extension") && i+1 < len(argv):
			i++
			loaded = loaded || isPiGuard(argv[i])
		case strings.HasPrefix(a, "--extension="):
			loaded = loaded || isPiGuard(strings.TrimPrefix(a, "--extension="))
		case a == "--extension-policy" && i+1 < len(argv):
			i++
			policy = argv[i]
		case strings.HasPrefix(a, "--extension-policy="):
			policy = strings.TrimPrefix(a, "--extension-policy=")
		}
	}
	switch {
	case !loaded:
		return false, "the launch argv does not load nhm's pi guard extension (-e), which nhm writes into the instance dir's " +
			PiExtensionDir + "/ when dcg is on PATH"
	case policy != PiPolicy:
		return false, fmt.Sprintf("the launch argv's --extension-policy is %q: only %s lets nhm's pi guard run dcg without a prompt", policy, PiPolicy)
	}
	return true, ""
}

// piUnsupported are the guards pi has no place for.
var piUnsupported = map[string]string{
	RCH:           "pi's tool_call hook can block a command but not change it, so nhm's pi guard cannot route a build through rch (rch's cargo shim can: rch shim)",
	AgentMailStop: "Agent Mail installs no stop hook for pi, and nhm's pi guard runs none",
}

// checkPi: nhm's own extension holds pi's dcg guard, so the guard is
// configured when dcg is installed; VerifyLaunch checks that the launch
// loads the extension.
func checkPi(Env) []Result {
	dcg, err := exec.LookPath(DCGPath)
	return each("pi", func(g string) Result {
		if why, ok := piUnsupported[g]; ok {
			return Result{State: Unsupported, Detail: why}
		}
		if err != nil {
			return Result{State: Missing, Detail: "dcg is not installed; nhm's pi guard extension runs it on every bash call", Fix: fixFor("pi", g)}
		}
		return Result{State: Configured, Sources: []string{"nhm's pi guard extension"}, Detail: "pi loads it with -e at launch; it runs " + dcg}
	})
}
