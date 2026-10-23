package cli

import (
	"flag"
	"fmt"
	"io"
)

const tutorialUsage = `Usage: nhm tutorial [--skip-animations] [--slide N]

ntm's tutorial is an animated slide show about ntm on tmux. nhm points to
ACFS's onboarding lessons instead, which teach the flywheel on herdr.
Exits 0.
`

const tutorialText = `nhm has no tutorial of its own: ntm's is a slide show about ntm on tmux.
ACFS's onboarding lessons teach the flywheel on herdr:

  onboard          the lessons, from a menu
  onboard 5        herdr and its agent commands
  onboard list     the lessons you have done

ACFS installs the lessons in ~/.acfs/onboard/lessons. nhm help lists
nhm's verbs.
`

// runTutorial is ntm's tutorial for nhm: a pointer to the lessons (the
// operator's ruling, nhm-k9d, 2026-10-23). ntm's --skip-animations and
// --slide start its slide show, which nhm has not, so they are taken and
// do not change the pointer.
func runTutorial(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tutorial", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, tutorialUsage) }
	fs.Bool("skip-animations", false, "ntm's: skip the animations")
	fs.Int("slide", 0, "ntm's: start from this slide")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) > 0 {
		fmt.Fprint(stderr, tutorialUsage)
		return exitRefused
	}
	fmt.Fprint(stdout, tutorialText)
	return exitOK
}
