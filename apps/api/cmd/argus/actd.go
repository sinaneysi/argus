package main

// actd.go: `argus actd`, the capability executor (ADR-0022). It runs in its own compose service (profile actions)
// with its own volume argus-act; see internal/actd.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/actd"
	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/secret"
)

const actdHelp = `usage: argus actd [-once] [-file F]
       argus actd credential [-file F] set <family>/<name>   (the value on stdin, never an argument)
       argus actd credential [-file F] list
       argus actd verify-audit [-file F]

actd           the capability executor (ADR-0022): reads action requests from ARGUS_STATE_DIR/actions/in, checks
               the fixed refusal rules before every step, runs the approved capability steps, appends one
               hash-chained audit record per phase to ARGUS_ACT_DIR/audit/actions.jsonl and writes redacted results
               to actions/out. ARGUS_ACTIONS_MODE: off (default; every request refused with a reason), dry-run (plan
               only), on. Runs only in ARGUS_LAYOUT=volume. -once: handle the waiting requests once and exit.
credential     store or list the executor's credentials, sealed with its own key (keys/act.key or ARGUS_ACT_KEY).
               Only through "docker compose exec -T actd argus actd credential set <name>".
verify-audit   check the audit chain: exit 0 and "OK" when it verifies, 1 when it is broken.
`

func runActd(args []string, stdout, stderr io.Writer) int {
	return runActdIO(args, os.Stdin, stdout, stderr)
}

func runActdIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if isHelp(args) {
		fmt.Fprint(stdout, actdHelp)
		return 0
	}
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("actd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	once := fs.Bool("once", false, "handle the waiting requests once and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	v := func(k string) string { return c.Values[k].Raw }
	mode, err := capability.CheckStart(v("ARGUS_LAYOUT"), v("ARGUS_ACTIONS_MODE"))
	if err != nil {
		fmt.Fprintln(stderr, "argus actd: does not start:", err)
		return 1
	}
	actDir := v("ARGUS_ACT_DIR")
	switch sub {
	case "verify-audit":
		head, n, err := actd.VerifyFile(actd.AuditPath(actDir))
		if err != nil {
			fmt.Fprintf(stdout, "BROKEN after %d records: %v\n", n, err)
			return 1
		}
		if st, ok := actd.ReadStatus(actd.StatusPath(actionsDir(c))); ok && st.Chain.Records > n {
			fmt.Fprintf(stdout, "BROKEN: %d records, the last status recorded %d (lines were removed)\n", n, st.Chain.Records)
			return 1
		}
		fmt.Fprintf(stdout, "OK %d records, head %s\n", n, short(head))
		return 0
	case "credential":
		return runActdCredential(fs.Args(), actDir, v("ARGUS_ACT_KEY"), stdin, stdout, stderr)
	case "":
	default:
		fmt.Fprint(stderr, actdHelp)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(stderr, actdHelp)
		return 2
	}
	o, err := actdOptions(c, mode, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "argus actd: does not start:", err)
		return 1
	}
	d, err := actd.New(o)
	if err != nil {
		fmt.Fprintln(stderr, "argus actd: does not start:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		d.Tick(ctx)
		return 0
	}
	o.Log.Info("actd started", "mode", string(mode), "capabilities", strings.Join(o.Registry.Names(), ","))
	if err := d.Run(ctx); err != nil {
		fmt.Fprintln(stderr, "argus actd:", err)
		return 1
	}
	return 0
}

func actionsDir(c *config.Config) string { return filepath.Join(c.StateDir, "actions") }

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "-"
	}
	return s
}

func actdOptions(c *config.Config, mode capability.Mode, stderr io.Writer) (actd.Options, error) {
	v := func(k string) string { return c.Values[k].Raw }
	ttl, err := capability.ParseTTL(v("ARGUS_ACTIONS_APPROVAL_TTL"))
	if err != nil {
		return actd.Options{}, err
	}
	perDay, _ := strconv.Atoi(v("ARGUS_ACTIONS_PER_DAY"))
	if err := capability.ValidPerDay(perDay); err != nil {
		return actd.Options{}, err
	}
	reg, err := capability.Default(capability.Options{HTTPHosts: splitList(v("ARGUS_ACT_HTTP_HOSTS")), HTTPAllowPlain: v("ARGUS_ACT_HTTP_ALLOW_HTTP") == "1"})
	if err != nil {
		return actd.Options{}, err
	}
	loc, err := time.LoadLocation(c.TZ)
	if err != nil {
		loc = time.UTC
	}
	actDir := v("ARGUS_ACT_DIR")
	o := actd.Options{Mode: mode, ActDir: actDir, ActionsDir: actionsDir(c), BoardDB: c.BoardDB, AuthDB: c.AuthDB, OpsDB: c.DB,
		Registry: reg, TTL: ttl, PerDay: perDay, Location: loc, Version: version,
		Log: slog.New(slog.NewTextHandler(stderr, nil)).With("job", "actd")}
	if mode != capability.ModeOff {
		if err := actd.CheckActDir(actDir); err != nil {
			return o, err
		}
		key, _, err := secret.Load(actd.KeySource(actDir, v("ARGUS_ACT_KEY"), true))
		if err != nil {
			return o, err
		}
		o.Credentials = func() []actd.Credential { return actd.ListCredentials(actDir, key) }
	}
	return o, nil
}

func runActdCredential(args []string, actDir, envKey string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, actdHelp)
		return 2
	}
	if err := actd.CheckActDir(actDir); err != nil {
		fmt.Fprintln(stderr, "argus actd credential:", err)
		return 1
	}
	switch {
	case args[0] == "set" && len(args) == 2:
		key, _, err := secret.Load(actd.KeySource(actDir, envKey, true))
		if err != nil {
			fmt.Fprintln(stderr, "argus actd credential:", err)
			return 1
		}
		b, err := io.ReadAll(io.LimitReader(stdin, actd.MaxCredentialBytes+2))
		if err != nil {
			fmt.Fprintln(stderr, "argus actd credential: read stdin:", err)
			return 1
		}
		if err := actd.SetCredential(actDir, key, args[1], string(b)); err != nil {
			fmt.Fprintln(stderr, "argus actd credential:", err)
			return 1
		}
		fmt.Fprintf(stdout, "stored %s\n", args[1])
		return 0
	case args[0] == "list" && len(args) == 1:
		if m, _ := filepath.Glob(filepath.Join(actDir, "secrets", "*", "*.enc")); len(m) == 0 {
			fmt.Fprintln(stdout, "no credentials stored")
			return 0
		}
		key, _, err := secret.Load(actd.KeySource(actDir, envKey, false))
		if err != nil {
			fmt.Fprintln(stderr, "argus actd credential:", err)
			return 1
		}
		for _, cr := range actd.ListCredentials(actDir, key) {
			last := cr.Last4
			if last == "" {
				last = "-"
			}
			line := fmt.Sprintf("%-40s ...%s  %s", cr.Name, last, time.Unix(cr.Updated, 0).UTC().Format(time.RFC3339))
			if cr.Error != "" {
				line += "  " + cr.Error
			}
			fmt.Fprintln(stdout, line)
		}
		return 0
	}
	fmt.Fprint(stderr, actdHelp)
	return 2
}
