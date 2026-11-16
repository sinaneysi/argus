package main

// actd.go: `argus actd`, the capability executor (ADR-0022). It runs in its own compose service (profile actions)
// with its own volume argus-act; see internal/actd.

import (
	"context"
	"crypto/ed25519"
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
       argus actd approval-key [-file F] set     (the api's approval public key, PEM on stdin)
       argus actd approval-key [-file F] show
       argus actd seal-key [-file F] public     (actd's sealing public key, PEM, for the api's pin)
       argus actd seal-key [-file F] show       (its fingerprint)
       argus actd status-key [-file F] public   (actd's status signing public key, PEM, for the api's pin)
       argus actd status-key [-file F] show     (its fingerprint)

actd           the capability executor (ADR-0022): reads action requests from ARGUS_STATE_DIR/actions/in, checks
               the fixed refusal rules before every step, runs the approved capability steps, appends one
               hash-chained audit record per phase to ARGUS_ACT_DIR/audit/actions.jsonl and writes redacted results
               to actions/out. ARGUS_ACTIONS_MODE: off (default; every request refused with a reason), dry-run (plan
               only), on. Runs only in ARGUS_LAYOUT=volume. -once: handle the waiting requests once and exit.
credential     store or list the executor's credentials, sealed with its own key (keys/act.key in ARGUS_ACT_DIR only).
               Only through "docker compose exec -T actd argus actd credential set <name>".
verify-audit   check the audit chain: exit 0 and "OK" when it verifies, 1 when it is broken.
approval-key   store or show the api's approval public key (keys/approval.pub in ARGUS_ACT_DIR). Without it every
               step is refused. Copy it from the api once:
               docker compose exec api /app/argus approval-key public | docker compose exec -T actd /app/argus actd approval-key set
seal-key       actd's X25519 sealing key (keys/act-x25519.key, created at the first start; the public half is
               published to actions/actd.pub). The api seals the secret inputs of an approval to it, after the key is
               pinned once:
               docker compose exec -T actd /app/argus actd seal-key public | docker compose exec -T api /app/argus approval-key trust-actd
status-key     actd's Ed25519 status key (keys/status.key, created at the first start). actd signs actions/actd-status.json
               with it; Settings shows the heartbeat as verified only after the key is pinned once:
               docker compose exec -T actd /app/argus actd status-key public | docker compose exec -T api /app/argus approval-key trust-actd-status
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
		key, _, err := secret.Load(actd.KeySource(actDir, false))
		if err != nil {
			if _, serr := os.Stat(actd.AuditPath(actDir)); serr != nil {
				fmt.Fprintln(stdout, "OK 0 records, head -")
				return 0
			}
			fmt.Fprintln(stdout, "BROKEN: the executor key is missing while the chain exists")
			return 1
		}
		head, n, err := actd.VerifyDir(actDir, key)
		if err != nil {
			fmt.Fprintf(stdout, "BROKEN after %d records: %v\n", n, err)
			return 1
		}
		fmt.Fprintf(stdout, "OK %d records, head %s\n", n, short(head))
		return 0
	case "credential":
		return runActdCredential(fs.Args(), actDir, stdin, stdout, stderr)
	case "approval-key":
		return runActdApprovalKey(fs.Args(), actDir, stdin, stdout, stderr)
	case "seal-key":
		return runActdSealKey(fs.Args(), actDir, stdout, stderr)
	case "status-key":
		return runActdStatusKey(fs.Args(), actDir, stdout, stderr)
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
	defer d.Close() // New holds the executor lock: -once and the daemon never run at the same time
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
	backupDays, _ := strconv.Atoi(v("ARGUS_ACTIONS_BACKUP_DAYS"))
	loc, err := time.LoadLocation(c.TZ)
	if err != nil {
		loc = time.UTC
	}
	actDir := v("ARGUS_ACT_DIR")
	o := actd.Options{Mode: mode, ActDir: actDir, ActionsDir: actionsDir(c), BoardDB: c.BoardDB, AuthDB: c.AuthDB, OpsDB: c.DB,
		TTL: ttl, PerDay: perDay, Location: loc, Version: version, BackupDays: backupDays,
		Log: slog.New(slog.NewTextHandler(stderr, nil)).With("job", "actd")}
	dirErr := actd.CheckActDir(actDir)
	if dirErr != nil && mode != capability.ModeOff {
		return o, dirErr
	}
	ro := registryOptions(c)
	if dirErr == nil {
		key, _, err := secret.Load(actd.KeySource(actDir, true))
		if err != nil {
			return o, err
		}
		o.Key = key
		o.Credentials = func() []actd.Credential { return actd.ListCredentials(actDir, key) }
		pub, err := actd.LoadApprovalPub(actDir)
		if err != nil {
			return o, fmt.Errorf("%s: %v", actd.ApprovalPubPath(actDir), err)
		}
		if pub == nil && mode != capability.ModeOff {
			fmt.Fprintln(stderr, "argus actd: no approval public key (argus actd approval-key set): every step is refused")
		}
		o.ApprovalPub = pub
		if o.SealKey, err = actd.LoadSealKey(actDir, true); err != nil {
			return o, fmt.Errorf("%s: %v", actd.SealKeyPath(actDir), err)
		}
		if o.StatusKey, err = actd.LoadStatusKey(actDir, true); err != nil {
			return o, fmt.Errorf("%s: %v", actd.StatusKeyPath(actDir), err)
		}
		// the GitLab token is opened for each call, from argus-act only; the old values go to argus-act/backups
		ro.GitLab.Token = func() (string, error) { return actd.OpenCredential(actDir, key, capability.GitLabCredential) }
		// the identity provider's client secret, from argus-act only (keycloak.read)
		ro.Keycloak.Secret = func() (string, error) { return actd.OpenCredential(actDir, key, capability.KeycloakCredential) }
		ro.GitLab.Backups = actd.Vault{Dir: filepath.Join(actDir, "backups"), Key: key, Steps: filepath.Join(actDir, "steps")}
	}
	reg, err := capability.Default(ro)
	if err != nil {
		return o, err
	}
	o.Registry = reg
	o.DevelopLabels = capability.ParseDevelopLabels(c.Values["ARGUS_ACT_DEVELOP_LABELS"].Raw)
	return o, nil
}

// registryOptions are the capabilities' allow-lists from the configuration (the worker and actd use the same).
func registryOptions(c *config.Config) capability.Options {
	v := func(k string) string { return c.Values[k].Raw }
	plain := v("ARGUS_ACT_HTTP_ALLOW_HTTP") == "1"
	return capability.Options{HTTPHosts: splitList(v("ARGUS_ACT_HTTP_HOSTS")), HTTPAllowPlain: plain,
		GitLab: capability.GitLabOptions{URL: v("ARGUS_ACT_GITLAB_URL"), Projects: splitList(v("ARGUS_ACT_GITLAB_PROJECTS")), AllowPlain: plain},
		Keycloak: capability.KeycloakOptions{URL: v("ARGUS_ACT_KEYCLOAK_URL"), Realms: splitList(v("ARGUS_ACT_KEYCLOAK_REALMS")),
			ClientID: v("ARGUS_ACT_KEYCLOAK_CLIENT_ID"), ClientRealm: v("ARGUS_ACT_KEYCLOAK_CLIENT_REALM"), AllowPlain: plain,
			MaxReads: kcMaxReads(v("ARGUS_ACT_KEYCLOAK_MAX_READS"))}}
}

func runActdSealKey(args []string, actDir string, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "public" && args[0] != "show") {
		fmt.Fprint(stderr, actdHelp)
		return 2
	}
	if err := actd.CheckActDir(actDir); err != nil {
		fmt.Fprintln(stderr, "argus actd seal-key:", err)
		return 1
	}
	k, err := actd.LoadSealKey(actDir, true)
	if err != nil {
		fmt.Fprintln(stderr, "argus actd seal-key:", err)
		return 1
	}
	if args[0] == "show" {
		fmt.Fprintln(stdout, capability.SealFingerprint(k.PublicKey()))
		return 0
	}
	b, err := capability.SealPublicPEM(k.PublicKey())
	if err != nil {
		fmt.Fprintln(stderr, "argus actd seal-key:", err)
		return 1
	}
	stdout.Write(b)
	return 0
}

func runActdApprovalKey(args []string, actDir string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := actd.CheckActDir(actDir); err != nil {
		fmt.Fprintln(stderr, "argus actd approval-key:", err)
		return 1
	}
	switch {
	case len(args) == 1 && args[0] == "set":
		b, err := io.ReadAll(io.LimitReader(stdin, 4097))
		if err != nil {
			fmt.Fprintln(stderr, "argus actd approval-key: read stdin:", err)
			return 1
		}
		p, err := actd.SetApprovalPub(actDir, b)
		if err != nil {
			fmt.Fprintln(stderr, "argus actd approval-key:", err)
			return 1
		}
		fmt.Fprintf(stdout, "stored the approval public key %s (restart actd to use it)\n", capability.KeyFingerprint(p))
		return 0
	case len(args) == 1 && args[0] == "show":
		p, err := actd.LoadApprovalPub(actDir)
		if err != nil {
			fmt.Fprintln(stderr, "argus actd approval-key:", err)
			return 1
		}
		if p == nil {
			fmt.Fprintln(stdout, "no approval public key: every step is refused")
			return 1
		}
		fmt.Fprintln(stdout, capability.KeyFingerprint(p))
		return 0
	}
	fmt.Fprint(stderr, actdHelp)
	return 2
}

func runActdCredential(args []string, actDir string, stdin io.Reader, stdout, stderr io.Writer) int {
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
		key, _, err := secret.Load(actd.KeySource(actDir, true))
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
		key, _, err := secret.Load(actd.KeySource(actDir, false))
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

// kcMaxReads reads ARGUS_ACT_KEYCLOAK_MAX_READS; empty or unreadable: the default (the config check bounds it).
func kcMaxReads(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func runActdStatusKey(args []string, actDir string, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "public" && args[0] != "show") {
		fmt.Fprint(stderr, actdHelp)
		return 2
	}
	if err := actd.CheckActDir(actDir); err != nil {
		fmt.Fprintln(stderr, "argus actd status-key:", err)
		return 1
	}
	k, err := actd.LoadStatusKey(actDir, true)
	if err != nil {
		fmt.Fprintln(stderr, "argus actd status-key:", err)
		return 1
	}
	pub := k.Public().(ed25519.PublicKey)
	if args[0] == "show" {
		fmt.Fprintln(stdout, capability.KeyFingerprint(pub))
		return 0
	}
	b, err := capability.PublicKeyPEM(pub)
	if err != nil {
		fmt.Fprintln(stderr, "argus actd status-key:", err)
		return 1
	}
	stdout.Write(b)
	return 0
}
