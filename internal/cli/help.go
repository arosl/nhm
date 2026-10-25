package cli

import "strings"

const versionUsage = `Usage: nhm version [--short|-s] [--json]

Print nhm's version and commit, the ntm commit whose grammar it tracks,
and the herdr protocol it speaks. Line 1 is ntm's shape, "nhm version
<version>"; line 2 holds the commit, the herdr protocol and the ntm
commit. --short prints the version alone, as ntm's does.
`

// verbUsage is each verb's usage text. A verb with sub-verbs shares one
// text, except admin (see adminUsage).
var verbUsage = map[string]string{
	"version":       versionUsage,
	"resolve":       resolveUsage,
	"adopt":         adoptUsage,
	"move":          moveUsage,
	"arrange":       arrangeUsage,
	"retire":        retireUsage,
	"recycle":       recycleUsage,
	"reap":          reapUsage,
	"kill":          killUsage,
	"attach":        attachUsage,
	"zoom":          zoomUsage,
	"view":          viewUsage,
	"tile":          viewUsage,
	"v":             viewUsage,
	"diff":          diffUsage,
	"copy":          copyUsage,
	"cp":            copyUsage,
	"yank":          copyUsage,
	"grep":          grepUsage,
	"errors":        errorsUsage,
	"switch":        switchUsage,
	"spawn":         spawnUsage,
	"add":           spawnUsage,
	"new":           newUsage,
	"status":        statusUsage,
	"list":          listUsage,
	"snapshot":      snapshotUsage,
	"attention":     attentionUsage,
	"read":          readUsage,
	"logs":          logsUsage,
	"watch":         watchUsage,
	"w":             watchUsage,
	"activity":      activityUsage,
	"health":        healthUsage,
	"doctor":        doctorUsage,
	"deps":          depsUsage,
	"check":         depsUsage,
	"tutorial":      tutorialUsage,
	"wake":          wakeUsage,
	"maintain":      maintainUsage,
	"mail":          mailVerbUsage,
	"mail inbox":    mailUsage,
	"capabilities":  capabilitiesUsage,
	"compatibility": compatibilityUsage,
	"quota":         quotaUsage,
	"limits":        limitsUsage,
	"palette":       paletteUsage,
	"board":         paletteUsage,
	"dashboard":     paletteUsage,
	"dash":          paletteUsage,
	"d":             paletteUsage,
	"menu":          paletteUsage,
	"bind":          bindUsage,
	"ui":            uiUsage,
	"wait":          waitUsage,
	"interrupt":     interruptUsage,
	"send":          sendUsage,
	"memory":        memoryUsage,
	"feedback":      feedbackUsage,
	"modes":         modesUsage,
	"template":      templateUsage,
	"ensemble":      ensembleUsage,

	// ntm's get-all-session-text and its aliases.
	"get-all-session-text": gastUsage,
	"gast":                 gastUsage,
	"swarm-status":         gastUsage,
}

// adminUsage is the usage text of the admin verb that args (after
// "admin") name, or of all of them.
func adminUsage(args []string) string {
	switch {
	case len(args) >= 1 && args[0] == "instance":
		return enrollUsage
	case len(args) >= 2 && args[0] == "recover" && args[1] == "reconcile":
		return recoverUsage
	case len(args) >= 2 && args[0] == "recover" && (args[1] == "inspect" || args[1] == "resume"):
		return adminLifecycleUsage
	case len(args) >= 1 && args[0] == "recover":
		return adminLifecycleUsage + "\n" + recoverUsage
	case len(args) >= 1 && (args[0] == "restore" || args[0] == "export" || args[0] == "import"):
		return adminLifecycleUsage
	case len(args) >= 1 && args[0] == "takeover":
		return takeoverUsage
	}
	return strings.Join([]string{enrollUsage, recoverUsage, adminLifecycleUsage, takeoverUsage}, "\n")
}

// leafHelp is the usage text for args (a verb and its arguments) when
// they ask for help (asksHelp). Run prints it and exits 0 before the verb
// runs, so a help request never acts (nhm-4cb: quota record-claude
// --help read stdin and saved it).
func leafHelp(args []string) (string, bool) {
	if len(args) < 2 || !asksHelp(args[1:]) {
		return "", false
	}
	if args[0] == "admin" {
		return adminUsage(args[1:]), true
	}
	u, ok := verbUsage[args[0]]
	return u, ok
}

// boolFlags are common flags that take no value, so a help flag after
// one is a flag of its own. TestHelpBoolFlagsTakeNoValue keeps the list
// true for every verb.
var boolFlags = map[string]bool{"json": true, "jsonl": true, "dry-run": true, "yes": true, "force": true,
	"all": true, "once": true, "loop": true, "watch": true, "verbose": true}

// asksHelp reports whether args ask for help: -h or --help, as Go's flag
// package spells them, before any "--", and in flag position, not as the
// value of the flag before it (nhm-ceu: `nhm send s --msg --help` sends
// "--help"). A help flag is in flag position first, after an argument,
// after a --flag=value, or after a flag in boolFlags. After another bare
// flag Run cannot tell, and the verb's own parser decides: that flag's
// value, or its help on stderr with exit 2.
func asksHelp(args []string) bool {
	for i, a := range args {
		if a == "--" {
			return false
		}
		if !isHelpFlag(a) {
			continue
		}
		if i == 0 {
			return true
		}
		prev := args[i-1]
		if !strings.HasPrefix(prev, "-") || prev == "-" || strings.Contains(prev, "=") || isHelpFlag(prev) ||
			boolFlags[strings.TrimLeft(prev, "-")] {
			return true
		}
	}
	return false
}

func isHelpFlag(a string) bool {
	switch a {
	case "-h", "--h", "-help", "--help":
		return true
	}
	return false
}
