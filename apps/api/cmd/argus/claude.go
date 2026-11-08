package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sinaneysi/argus/internal/claudegate"
	"github.com/sinaneysi/argus/internal/claudetoken"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/secret"
)

const secretsHelp = `usage: argus secrets apply-claude|apply-kube|install-kubectl|rekey|new-key [-file PATH]

apply-claude  root, run by argus-claude-token.path (examples/systemd): processes the requests Settings -> Claude
              writes into ARGUS_SETTINGS_IN/claude-token/: replace the Claude token in ARGUS_CLAUDE_ENV_FILE (backup
              <file>.bak-<time>-claude, the last 5 kept; the previous token is restored when the one test call fails,
              unless the admin chose "save anyway"), test the token in place, refresh the egress-country check, and
              apply the egress guard settings (ARGUS_CLAUDE_EGRESS_FILE). Status: ARGUS_SETTINGS_OUT/
              claude-token-status.json; audit lines in ARGUS_SETTINGS_OUT/audit.log. The token is never printed.
              Refused when DEMO_MODE=1.
`

const claudeHelp = `usage: argus claude check [-file PATH]
       argus claude egress [-force] [-file PATH]

check   exit 0 when a Claude run may start now: the egress-country guard (Settings -> Claude) lets it; else the
        reason on stderr and exit 78 (a skip, not a failure). Every Claude runner calls this first.
egress  the egress check as JSON (cached 10 minutes; -force looks it up again); exit 0 allowed, 78 stopped.
The lookup uses the proxy settings the Claude runs get: HTTPS_PROXY, HTTP_PROXY, NO_PROXY of ARGUS_CLAUDE_ENV_FILE.
`

// skipExit is the exit status of a stopped run: distinct from claude's own exits and timeout's 124.
const skipExit = 78

func loadQuiet(file string, stderr io.Writer) (*config.Config, bool) {
	return loadForChecks(file, stderr)
}

func runSecrets(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args[:1]) {
		fmt.Fprint(stdout, secretsHelp+kubeSecretsHelp+rekeyHelp)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] == "apply-kube" || args[0] == "install-kubectl" {
		return runSecretsKube(args[0], args[1:], stdout, stderr)
	}
	switch args[0] {
	case "rekey":
		return runRekey(args[1:], stdout, stderr)
	case "new-key":
		fmt.Fprintln(stdout, secret.NewKeyText())
		return 0
	}
	if args[0] != "apply-claude" {
		fmt.Fprintf(stderr, "argus secrets: unknown subcommand %q (apply-claude, apply-kube, install-kubectl, rekey, new-key)\n", args[0])
		return 2
	}
	fs := flag.NewFlagSet("secrets apply-claude", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus secrets apply-claude: disabled in the demo (DEMO_MODE=1): nothing was changed")
		return 1
	}
	o := claudeOptions(c)
	o.Log = stdout
	if err := claudetoken.Apply(o); err != nil {
		fmt.Fprintln(stderr, "argus secrets apply-claude: status not written:", err)
		return 1
	}
	return 0
}

func claudeOptions(c *config.Config) claudetoken.Options {
	v := func(k string) string { return c.Values[k].Raw }
	return claudetoken.Options{
		ReqDir: filepath.Join(c.SettingsIn, "claude-token"), OutDir: c.SettingsOut, EnvFile: v("ARGUS_CLAUDE_ENV_FILE"),
		AuthDB: c.AuthDB, RunAs: v("ARGUS_CLAUDE_RUN_AS"), ClaudeBin: v("ARGUS_CLAUDE_BIN"),
		EgressFile: v("ARGUS_CLAUDE_EGRESS_FILE"), EgressCheck: v("ARGUS_CLAUDE_EGRESS_CHECK"),
		EgressURLs: splitList(v("ARGUS_CLAUDE_EGRESS_URLS")), AppUID: 65534, Key: volumeKey(c),
	}
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func runClaude(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args[:1]) {
		fmt.Fprint(stdout, claudeHelp)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub := args[0]
	if sub != "check" && sub != "egress" {
		fmt.Fprintf(stderr, "argus claude: unknown subcommand %q (check, egress)\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("claude "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	force := fs.Bool("force", false, "look the country up again (egress)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	o := claudeOptions(c)
	env := map[string]string{}
	if data, err := os.ReadFile(o.EnvFile); err == nil {
		for k, val := range claudetoken.ParseEnv(string(data)) {
			if strings.HasSuffix(strings.ToUpper(k), "_PROXY") {
				env[k] = val
			}
		}
	}
	g := &claudegate.Gate{SettingsFile: o.EgressFile, CacheFile: o.EgressCheck, URLs: o.EgressURLs, Env: env}
	allowed, reason, check := g.Guard(sub == "egress" && *force)
	if sub == "egress" {
		b, _ := json.MarshalIndent(check, "", " ")
		fmt.Fprintln(stdout, string(b))
	} else if !allowed {
		fmt.Fprintln(stderr, reason)
	}
	if !allowed {
		return skipExit
	}
	return 0
}
