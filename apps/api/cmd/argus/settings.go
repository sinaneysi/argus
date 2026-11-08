package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/settingsapply"
)

const settingsHelp = `usage: argus settings apply [-file PATH]
       argus settings claude [on|off] [-file PATH]
       argus settings qa [on|off|dry-run|send|images-on|images-off ...] [-file PATH]

apply   root, run by argus-settings.path (examples/systemd) when the web app saves Settings -> General: validates
        ARGUS_SETTINGS_IN/settings.json as a whole and applies it, or rejects it and keeps what is live (exit 1; the
        page shows "Last save was rejected" with the reason). Writes in ARGUS_ETC_DIR: thresholds.env, rca-auto.json,
        readiness.json, qa-ask.json, claude.json, and ARGUS_CLAUDE_EGRESS_FILE when settings.json has
        "claude_egress". Status in ARGUS_SETTINGS_OUT: applied.json, qa-ask.json, claude.json, claude-egress.json,
        audit.log. Schedules change the systemd timers <ARGUS_SETTINGS_TIMER_PREFIX><task>.timer (drop-in
        schedule.conf, enable/disable); with the prefix empty no timer is touched. Refused when DEMO_MODE=1.
claude  print the Claude usage switch, or switch it (ARGUS_ETC_DIR/claude.json and the live copy).
qa      print the QA follow-up question settings, or change them (ARGUS_ETC_DIR/qa-ask.json and the live copy).
`

func runSettings(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args[:1]) {
		fmt.Fprint(stdout, settingsHelp)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub := args[0]
	if sub != "apply" && sub != "claude" && sub != "qa" {
		fmt.Fprintf(stderr, "argus settings: unknown subcommand %q (apply, claude, qa)\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("settings "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	var words []string
	rest := args[1:]
	for len(rest) > 0 { // words and flags in any order
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if rest = fs.Args(); len(rest) > 0 {
			words, rest = append(words, rest[0]), rest[1:]
		}
	}
	if sub == "apply" && len(words) > 0 {
		fmt.Fprintf(stderr, "argus settings apply: unexpected argument %q\n", words[0])
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintf(stderr, "argus settings %s: disabled in the demo (DEMO_MODE=1): nothing was changed\n", sub)
		return 1
	}
	o := settingsOptions(c)
	o.Log, o.Err = stdout, stderr
	switch sub {
	case "claude":
		return settingsapply.ClaudeCLI(o, words)
	case "qa":
		return settingsapply.QA(o, words)
	}
	if err := settingsapply.Apply(o); err != nil {
		if !settingsapply.IsRejected(err) {
			fmt.Fprintln(stderr, "argus settings apply: failed part way:", err)
		}
		return 1
	}
	return 0
}

func settingsOptions(c *config.Config) settingsapply.Options {
	v := func(k string) string { return c.Values[k].Raw }
	return settingsapply.Options{
		In: filepath.Join(c.SettingsIn, "settings.json"), OutDir: c.SettingsOut, EtcDir: c.EtcDir,
		EgressFile: v("ARGUS_CLAUDE_EGRESS_FILE"), TimerPrefix: v("ARGUS_SETTINGS_TIMER_PREFIX"),
	}
}
