package cli

import "strings"

const versionUsage = `Usage: nhm version [--json]

Print nhm's version and commit, the ntm commit whose grammar it tracks,
and the herdr protocol it speaks.
`

// verbUsage is each verb's usage text. A verb with sub-verbs shares one
// text, except admin (see adminUsage).
var verbUsage = map[string]string{
	"version":       versionUsage,
	"resolve":       resolveUsage,
	"adopt":         adoptUsage,
	"move":          moveUsage,
	"retire":        retireUsage,
	"recycle":       recycleUsage,
	"reap":          reapUsage,
	"kill":          killUsage,
	"attach":        attachUsage,
	"zoom":          zoomUsage,
	"spawn":         spawnUsage,
	"add":           spawnUsage,
	"new":           newUsage,
	"status":        statusUsage,
	"list":          listUsage,
	"snapshot":      snapshotUsage,
	"attention":     attentionUsage,
	"read":          readUsage,
	"watch":         watchUsage,
	"activity":      activityUsage,
	"health":        healthUsage,
	"doctor":        doctorUsage,
	"wake":          wakeUsage,
	"maintain":      maintainUsage,
	"mail":          mailUsage,
	"capabilities":  capabilitiesUsage,
	"compatibility": compatibilityUsage,
	"quota":         quotaUsage,
	"limits":        limitsUsage,
	"palette":       paletteUsage,
	"board":         paletteUsage,
	"menu":          paletteUsage,
	"bind":          bindUsage,
	"ui":            uiUsage,
	"wait":          waitUsage,
	"interrupt":     interruptUsage,
	"send":          sendUsage,
	"memory":        memoryUsage,
	"feedback":      feedbackUsage,
	"modes":         modesUsage,
	"ensemble":      ensembleUsage,
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
	case len(args) >= 1 && (args[0] == "takeover" || args[0] == "handback"):
		return takeoverUsage
	}
	return strings.Join([]string{enrollUsage, recoverUsage, adminLifecycleUsage, takeoverUsage}, "\n")
}

// leafHelp is the usage text for args (a verb and its arguments) when
// they ask for help: -h or --help, as Go's flag package spells them,
// before any "--". Run prints it and exits 0 before the verb runs, so a
// help request never acts (nhm-4cb: quota record-claude --help read
// stdin and saved it).
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

func asksHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "--h", "-help", "--help":
			return true
		}
	}
	return false
}
