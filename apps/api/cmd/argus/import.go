package main

// import.go: argus import (ADR-0021 P1). A one-time, idempotent import of an earlier installation's state into the
// one volume: the three databases, the state files, the saved settings and their results, the reports, the 2FA key
// (2FA secrets re-encrypted under this product's label), and the integrations configured in its env files and its
// etc folder, as Settings -> Data sources entries with their credentials sealed by the master key. The source is only
// read. Nothing is printed but names: never a value.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/board"
	"github.com/sinaneysi/argus/internal/claudetoken"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/secret"
	"github.com/sinaneysi/argus/internal/sources"
)

const importHelp = `usage: argus import [-dry-run] [-force] [-settings-only] [-with-googlebot] [-etc DIR] [-env FILE]... [-reports DIR]
                    [-auth DB] [-totp-legacy-label L] [-file PATH] <state dir>

One-time import of an earlier installation into the one volume (ARGUS_LAYOUT=volume; run it before the first start
of the api and the worker, or with them stopped). <state dir> is the earlier installation's state folder: ops.db,
board/board.db, auth/auth.db, the *.json state files, settings-in/ and settings-out/. It is only read.

  -etc DIR      its configuration folder: totp.key, gitlab-issues.json/.token/-allowed-hosts, jira.env, jira.pat,
                outline.json/.token, kube/ (clusters.json and kubeconfigs)
  -env FILE     a KEY=value file of the earlier installation (repeatable; the first file that sets a key wins):
                GITLAB_URL + GITLAB_RO_TOKEN, MATRIX_HOMESERVER + MATRIX_ACCESS_TOKEN, PROM_URL, GRAFANA_<ID>_URL +
                GRAFANA_<ID>_TOKEN, MC_HOST_<alias> (S3 of the backup check), CLAUDE_CODE_OAUTH_TOKEN
  -reports DIR  its reports folder (the RCA reports)
  -auth DB      the users database to import instead of <state dir>/auth/auth.db
  -totp-legacy-label L  the label the 2FA secrets were written under (default: TOTP_LEGACY_LABEL); they are
                re-encrypted under this product's label, so TOTP_LEGACY_LABEL is not needed afterwards
  -dry-run      print what would be imported (names only), change nothing
  -force        import into a volume that already holds data (the imported items replace what is there)
  -settings-only  write only the carried settings (ARGUS_ETC_DIR/imported.env, the GitLab issues allowed hosts):
                for a volume imported by an earlier version; the databases and every other file are left as they are
  -with-googlebot  also copy googlebot.db (the crawler log database, often large; skipped by default)

Carried settings: the site and integration keys of the env files that are not secret (names, time zone, GITLAB_URL,
the Matrix homeserver and rooms, JIRA_PROJECT, ...; config.Importable) go to ARGUS_ETC_DIR/imported.env, which the
volume layout reads below argus.env and the environment. A GitLab issues connection whose host is not the GITLAB_URL
host gets an allowed-hosts file with both.

The databases are copied consistently (VACUUM INTO), then migrated to this version's schema. Every credential
becomes a Settings -> Data sources entry sealed with the master key (ARGUS_MASTER_KEY, else ARGUS_MASTER_KEY_FILE,
created when missing); then every secret of the volume is sealed (as argus secrets rekey). Pending request files of
the earlier installation are not imported. Each new source shows "not checked yet" until the worker checks it.
`

type importer struct {
	c      *config.Config
	src    string
	etc    string
	authDB string
	env    map[string]string
	envSrc map[string]string
	dry    bool
	force  bool
	gbot   bool
	out    io.Writer
	done   []string
	skip   []string
	key    secret.Key
}

func (im *importer) did(format string, a ...any) {
	im.done = append(im.done, fmt.Sprintf(format, a...))
}
func (im *importer) skip_(format string, a ...any) {
	im.skip = append(im.skip, fmt.Sprintf(format, a...))
}

func runImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, importHelp) }
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	dry := fs.Bool("dry-run", false, "print what would be imported, change nothing")
	force := fs.Bool("force", false, "import into a volume that already holds data")
	etc := fs.String("etc", "", "the earlier installation's configuration folder")
	reports := fs.String("reports", "", "the earlier installation's reports folder")
	label := fs.String("totp-legacy-label", "", "the label of the earlier 2FA secrets")
	authDB := fs.String("auth", "", "the users database to import instead of <state dir>/auth/auth.db")
	settingsOnly := fs.Bool("settings-only", false, "write only the carried settings (imported.env, GitLab issues allowed hosts)")
	gbot := fs.Bool("with-googlebot", false, "also copy googlebot.db (large)")
	var envs multiFlag
	fs.Var(&envs, "env", "a KEY=value file of the earlier installation (repeatable)")
	if len(args) > 0 && isHelp(args[:1]) {
		fmt.Fprint(stdout, importHelp)
		return 0
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, importHelp)
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.Values["ARGUS_LAYOUT"].Raw != "volume" {
		fmt.Fprintln(stderr, "argus import: only into ARGUS_LAYOUT=volume")
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus import: disabled in the demo (DEMO_MODE=1)")
		return 1
	}
	src, _ := filepath.Abs(fs.Arg(0))
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		fmt.Fprintln(stderr, "argus import: not a folder:", src)
		return 1
	}
	if same(src, c.StateDir) {
		fmt.Fprintln(stderr, "argus import: the source is the volume itself")
		return 1
	}
	im := &importer{c: c, src: src, etc: *etc, authDB: *authDB, dry: *dry, force: *force, gbot: *gbot, out: stdout, env: map[string]string{}, envSrc: map[string]string{}}
	for _, p := range envs {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(stderr, "argus import: cannot read", p)
			return 1
		}
		for k, v := range sources.ParseEnv(string(b)) {
			if _, ok := im.env[k]; !ok && v != "" {
				im.env[k], im.envSrc[k] = v, filepath.Base(p)
			}
		}
	}
	if *settingsOnly {
		if !im.dry {
			if err := os.MkdirAll(c.EtcDir, 0o750); err != nil {
				fmt.Fprintln(stderr, "argus import:", err)
				return 1
			}
		}
		if err := im.carried(context.Background()); err != nil {
			fmt.Fprintln(stderr, "argus import:", err)
			im.print()
			return 1
		}
		im.print()
		return 0
	}
	if busy := im.occupied(); len(busy) > 0 && !im.force {
		fmt.Fprintln(stderr, "argus import: the volume already holds data (use -force to replace it):")
		for _, b := range busy {
			fmt.Fprintln(stderr, "  "+b)
		}
		return 1
	}
	if !im.dry {
		if err := migrate.EnsureDirs(c); err != nil {
			fmt.Fprintln(stderr, "argus import:", err)
			return 1
		}
		k, from, err := secret.ForVolume(func(k string) string { return c.Values[k].Raw }, true)
		if err != nil {
			fmt.Fprintln(stderr, "argus import:", err)
			return 1
		}
		if k == nil {
			if k, from, err = secret.Load(secret.Source{Env: c.Values["ARGUS_MASTER_KEY"].Raw, File: c.Values["ARGUS_MASTER_KEY_FILE"].Raw, Generate: true}); err != nil {
				fmt.Fprintln(stderr, "argus import:", err)
				return 1
			}
		}
		im.key = k
		im.did("master key: %s (back it up with the volume)", from)
	}
	if lbl := *label; lbl != "" {
		auth.LegacyLabel = lbl
	} else {
		auth.LegacyLabel = c.Values["TOTP_LEGACY_LABEL"].Raw
	}
	ctx := context.Background()
	steps := []func(context.Context) error{im.databases, im.stateFiles, im.settings, func(context.Context) error { return im.tree(*reports, c.ReportsDir, "reports") },
		im.totp, im.integrations, im.carried, im.otherDBs, im.sealAll}
	for _, st := range steps {
		if err := st(ctx); err != nil {
			fmt.Fprintln(stderr, "argus import:", err)
			im.print()
			return 1
		}
	}
	im.print()
	return 0
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func same(a, b string) bool {
	x, err1 := filepath.EvalSymlinks(a)
	y, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && x == y
}

func (im *importer) print() {
	w := im.out
	if im.dry {
		fmt.Fprintln(w, "dry run: nothing was changed")
	}
	verb := map[bool]string{true: "would import", false: "imported"}[im.dry]
	fmt.Fprintf(w, "%s (%d):\n", verb, len(im.done))
	for _, d := range im.done {
		fmt.Fprintln(w, "  "+d)
	}
	fmt.Fprintf(w, "skipped (%d):\n", len(im.skip))
	for _, s := range im.skip {
		fmt.Fprintln(w, "  "+s)
	}
}

// --- what the volume already holds -----------------------------------------------------------------------------------

func countRows(path, table string) int {
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0
	}
	defer db.Close()
	var n int
	if db.QueryRow("SELECT count(*) FROM "+table).Scan(&n) != nil {
		return 0
	}
	return n
}

// occupied names what the volume holds that an import would replace (empty databases of a first start do not count).
func (im *importer) occupied() []string {
	c := im.c
	var out []string
	for _, x := range [][2]string{{c.AuthDB, "users"}, {c.BoardDB, "tasks"}, {c.DB, "runs"}} {
		if n := countRows(x[0], x[1]); n > 0 {
			out = append(out, fmt.Sprintf("%s (%d %s)", x[0], n, x[1]))
		}
	}
	if m, _ := filepath.Glob(filepath.Join(c.StateDir, "*.json")); len(m) > 0 {
		var keep []string
		for _, p := range m {
			if filepath.Base(p) != "worker-status.json" {
				keep = append(keep, p)
			}
		}
		if len(keep) > 0 {
			out = append(out, fmt.Sprintf("%s (%d state files)", c.StateDir, len(keep)))
		}
	}
	if _, err := os.Stat(filepath.Join(c.Values["ARGUS_SOURCES_DIR"].Raw, "registry.json")); err == nil {
		out = append(out, filepath.Join(c.Values["ARGUS_SOURCES_DIR"].Raw, "registry.json"))
	}
	return out
}

// --- databases ---------------------------------------------------------------------------------------------------------

// copyDB copies an SQLite database consistently: the file and its journal into a scratch folder of the target, then
// VACUUM INTO the target. The source is only read.
func (im *importer) copyDB(ctx context.Context, from, to, name string) error {
	if fi, err := os.Stat(from); err != nil || !fi.Mode().IsRegular() {
		im.skip_("%s: no %s", name, rel(im.src, from))
		return nil
	}
	if im.dry {
		im.did("%s: %s -> %s", name, rel(im.src, from), to)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(to), ".import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	work := filepath.Join(tmp, "src.db")
	for _, sfx := range []string{"", "-wal"} {
		if _, err := os.Stat(from + sfx); err == nil {
			if err := copyFile(from+sfx, work+sfx, 0o600); err != nil {
				return err
			}
		}
	}
	db, err := sql.Open("sqlite", "file:"+work+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	out := filepath.Join(tmp, "out.db")
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", out)
	db.Close()
	if err != nil {
		return fmt.Errorf("%s: copy: %w", name, err)
	}
	for _, sfx := range []string{"-wal", "-shm", "-journal"} {
		os.Remove(to + sfx)
	}
	if err := os.Rename(out, to); err != nil {
		return err
	}
	os.Chmod(to, 0o640)
	im.did("%s: %s -> %s", name, rel(im.src, from), to)
	return nil
}

func (im *importer) databases(ctx context.Context) error {
	c := im.c
	authFrom := filepath.Join(im.src, "auth", "auth.db")
	if im.authDB != "" {
		authFrom = im.authDB
	}
	for _, x := range [][3]string{{filepath.Join(im.src, "ops.db"), c.DB, "evidence database"},
		{filepath.Join(im.src, "board", "board.db"), c.BoardDB, "board database"}, {authFrom, c.AuthDB, "users, roles and 2FA"}} {
		if err := im.copyDB(ctx, x[0], x[1], x[2]); err != nil {
			return err
		}
	}
	if im.dry {
		return nil
	}
	res, err := migrate.All(ctx, c, func(string, ...any) {})
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for _, r := range res {
		if len(r.Applied) > 0 {
			im.did("schema %s: %d migrations applied", r.DB, len(r.Applied))
		}
	}
	return nil
}

// --- files -------------------------------------------------------------------------------------------------------------

func rel(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		return err
	}
	tmp := to + ".import.tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chmod(tmp, mode)
	return os.Rename(tmp, to)
}

// stateFiles are the *.json state files at the top of the state folder.
func (im *importer) stateFiles(context.Context) error {
	m, _ := filepath.Glob(filepath.Join(im.src, "*.json"))
	sort.Strings(m)
	n := 0
	for _, p := range m {
		if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
			im.skip_("%s: not a regular file", rel(im.src, p))
			continue
		}
		if !im.dry {
			if err := copyFile(p, filepath.Join(im.c.StateDir, filepath.Base(p)), 0o644); err != nil {
				return err
			}
		}
		n++
	}
	im.did("state files: %d *.json -> %s", n, im.c.StateDir)
	return nil
}

// tree copies the regular files of a folder (no symlinks); skipDirs at the top are left out.
func (im *importer) tree(from, to, what string, skipDirs ...string) error {
	if from == "" {
		im.skip_("%s: no folder given", what)
		return nil
	}
	if fi, err := os.Stat(from); err != nil || !fi.IsDir() {
		im.skip_("%s: %s is not a folder", what, from)
		return nil
	}
	n, skipped := 0, 0
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r := rel(from, p)
		if d.IsDir() {
			top := strings.Split(r, string(os.PathSeparator))[0]
			for _, s := range skipDirs {
				if top == s && p != from {
					if es, _ := os.ReadDir(p); len(es) > 0 {
						skipped += len(es)
					}
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if strings.HasSuffix(p, ".tmp") {
			return nil
		}
		if !im.dry {
			mode := os.FileMode(0o640)
			if fi, err := d.Info(); err == nil && fi.Mode().Perm()&0o077 == 0 {
				mode = 0o600
			}
			if err := copyFile(p, filepath.Join(to, r), mode); err != nil {
				return err
			}
		}
		n++
		return nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	im.did("%s: %d files -> %s", what, n, to)
	if skipped > 0 {
		im.skip_("%s: %d pending request files (the earlier installation's requests are not replayed)", what, skipped)
	}
	return nil
}

// requestDirs are the request folders of settings-in: pending requests are not imported.
var requestDirs = append(append([]string{}, migrate.RequestDirs...), "requests", "fix-requests", "task-chat", "jira-connect",
	"matrix-dm", "proposals")

func (im *importer) settings(context.Context) error {
	if err := im.tree(filepath.Join(im.src, "settings-in"), im.c.SettingsIn, "settings-in", requestDirs...); err != nil {
		return err
	}
	return im.tree(filepath.Join(im.src, "settings-out"), im.c.SettingsOut, "settings-out")
}

// --- 2FA -----------------------------------------------------------------------------------------------------------------

func (im *importer) totp(context.Context) error {
	to := im.c.Values["TOTP_KEY_FILE"].Raw
	from := ""
	if im.etc != "" {
		from = filepath.Join(im.etc, "totp.key")
	}
	if from == "" {
		im.skip_("2FA key: no -etc folder (users with 2FA enrol again)")
		return nil
	}
	if _, err := os.Stat(from); err != nil {
		im.skip_("2FA key: no totp.key in the -etc folder (users with 2FA enrol again)")
		return nil
	}
	if im.dry {
		im.did("2FA key: totp.key -> %s; 2FA secrets re-encrypted under this product's label", to)
		return nil
	}
	if err := copyFile(from, to, 0o600); err != nil {
		return err
	}
	auth.DB, auth.KeyFile = im.c.AuthDB, to
	auth.ResetKeys()
	auth.Reset()
	n, err := auth.Rekey()
	auth.Reset()
	if err != nil && !errors.Is(err, auth.ErrNoKey) {
		return fmt.Errorf("2FA rekey: %w (is -totp-legacy-label right?)", err)
	}
	im.did("2FA key: totp.key -> %s; %d 2FA secrets re-encrypted", to, n)
	return nil
}

// --- integrations: Settings -> Data sources ----------------------------------------------------------------------------

var (
	grafanaURLRe = regexp.MustCompile(`^GRAFANA_([A-Z0-9_]{1,20})_URL$`)
	mcHostRe     = regexp.MustCompile(`^MC_HOST_([A-Za-z0-9_]{1,20})$`)
)

func (im *importer) etcFile(name string) string {
	if im.etc == "" {
		return ""
	}
	return strings.TrimSpace(sources.ReadSecret(filepath.Join(im.etc, name)))
}

func (im *importer) source(e sources.Entry, cred, from string) error {
	label := e.Type + "/" + e.Name
	if im.dry {
		im.did("data source %s (%s, purpose %s) from %s", label, e.URL, e.Purpose, from)
		return nil
	}
	o := sourcesOptions(im.c)
	o.Key = im.key
	added, err := o.Import(e, cred, im.force)
	if err != nil {
		return fmt.Errorf("data source %s: %w", label, err)
	}
	if added {
		im.did("data source %s (%s, purpose %s) from %s", label, e.URL, e.Purpose, from)
	} else {
		im.skip_("data source %s: already in Settings -> Data sources", label)
	}
	return nil
}

func (im *importer) integrations(context.Context) error {
	env, c := im.env, im.c
	etcDir := c.EtcDir
	// GitLab, read-only token for the agent
	if u, tok := env["GITLAB_URL"], env["GITLAB_RO_TOKEN"]; u != "" && tok != "" {
		if err := im.source(sources.Entry{Type: "gitlab", Name: "main", Env: "shared", URL: u, Purpose: "agent"}, tok, im.envSrc["GITLAB_RO_TOKEN"]); err != nil {
			return err
		}
	} else if u != "" {
		im.skip_("data source gitlab/main: GITLAB_URL without GITLAB_RO_TOKEN")
	}
	// GitLab issues tracker: its connection file, its token (sealed), its allowed hosts
	if meta := filepath.Join(im.etc, "gitlab-issues.json"); im.etc != "" && fileOK(meta) {
		tok := im.etcFile("gitlab-issues.token")
		if im.dry {
			im.did("GitLab issues connection: gitlab-issues.json%s -> %s", map[bool]string{true: " + token (sealed)", false: ""}[tok != ""], etcDir)
		} else {
			if err := copyFile(meta, filepath.Join(etcDir, "gitlab-issues.json"), 0o600); err != nil {
				return err
			}
			if h := filepath.Join(im.etc, "gitlab-issues-allowed-hosts"); fileOK(h) {
				copyFile(h, filepath.Join(etcDir, "gitlab-issues-allowed-hosts"), 0o644)
			}
			if tok != "" {
				s, err := im.key.Seal(board.GitLabTokenName, tok)
				if err != nil {
					return err
				}
				if err := secret.WriteFile(filepath.Join(etcDir, "gitlab-issues.token"), []byte(s)); err != nil {
					return err
				}
			}
			im.did("GitLab issues connection: gitlab-issues.json%s -> %s", map[bool]string{true: " + token (sealed)", false: ""}[tok != ""], etcDir)
		}
	}
	// Jira: the shared connection's settings (JIRA_* lines only) and its token
	if jf := filepath.Join(im.etc, "jira.env"); im.etc != "" && fileOK(jf) {
		b, _ := os.ReadFile(jf)
		je := sources.ParseEnv(string(b))
		var lines []string
		for _, l := range strings.Split(string(b), "\n") {
			k, _, ok := strings.Cut(strings.TrimSpace(l), "=")
			k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
			if ok && (strings.HasPrefix(k, "JIRA_") || strings.HasPrefix(k, "BOARD_") || k == "FIXER_TRIGGER") && !strings.Contains(k, "TOKEN") && !strings.Contains(k, "PASS") {
				lines = append(lines, strings.TrimSpace(l))
			}
		}
		if !im.dry {
			if err := secret.WriteFile(filepath.Join(etcDir, "jira.env"), []byte(strings.Join(lines, "\n")+"\n")); err != nil {
				return err
			}
		}
		im.did("Jira settings: %d JIRA_* lines of jira.env -> %s", len(lines), filepath.Join(etcDir, "jira.env"))
		if tok := im.etcFile("jira.pat"); je["JIRA_URL"] != "" && tok != "" {
			user := je["JIRA_USER"]
			if user == "" {
				user = je["JIRA_USERNAME"]
			}
			if err := im.source(sources.Entry{Type: "jira", Name: "main", Env: "shared", URL: je["JIRA_URL"], Purpose: "tracker", Username: user}, tok, "jira.env + jira.pat"); err != nil {
				return err
			}
		}
	}
	// Outline: its settings file and token
	if of := filepath.Join(im.etc, "outline.json"); im.etc != "" && fileOK(of) {
		var m map[string]any
		b, _ := os.ReadFile(of)
		json.Unmarshal(b, &m)
		if u, _ := m["url"].(string); u != "" {
			if err := im.source(sources.Entry{Type: "outline", Name: "wiki", Env: "shared", URL: u, Purpose: "docs"}, im.etcFile("outline.token"), "outline.json + outline.token"); err != nil {
				return err
			}
			if !im.dry {
				copyFile(of, filepath.Join(etcDir, "outline.json"), 0o600)
			}
		}
	}
	// Matrix
	if hs, tok := env["MATRIX_HOMESERVER"], env["MATRIX_ACCESS_TOKEN"]; hs != "" && tok != "" {
		if err := im.source(sources.Entry{Type: "matrix", Name: "main", Env: "shared", URL: hs, Purpose: "notify"}, tok, im.envSrc["MATRIX_ACCESS_TOKEN"]); err != nil {
			return err
		}
	}
	// Alertmanager / Prometheus
	for _, k := range []string{"ALERTMANAGER_URL", "PROM_URL", "PROMETHEUS_URL"} {
		if u := env[k]; u != "" {
			if err := im.source(sources.Entry{Type: "alertmanager", Name: "main", Env: "shared", URL: u, Purpose: "alerts"}, "", im.envSrc[k]+" "+k); err != nil {
				return err
			}
			break
		}
	}
	// Grafana sites
	var gks []string
	for k := range env {
		if grafanaURLRe.MatchString(k) {
			gks = append(gks, k)
		}
	}
	sort.Strings(gks)
	for _, k := range gks {
		id := grafanaURLRe.FindStringSubmatch(k)[1]
		tok := env["GRAFANA_"+id+"_TOKEN"]
		name := strings.Trim(strings.ReplaceAll(strings.ToLower(id), "_", "-"), "-")
		if tok == "" || !sources.NameRe.MatchString(name) {
			im.skip_("data source grafana/%s: no GRAFANA_%s_TOKEN", name, id)
			continue
		}
		if err := im.source(sources.Entry{Type: "grafana", Name: name, Env: "shared", URL: env[k], Purpose: "agent"}, tok, im.envSrc[k]); err != nil {
			return err
		}
	}
	// S3 / MinIO of the backup check: MC_HOST_<alias>=https://<access key>:<secret key>@<host>
	var mks []string
	for k := range env {
		if mcHostRe.MatchString(k) {
			mks = append(mks, k)
		}
	}
	sort.Strings(mks)
	for _, k := range mks {
		name := strings.Trim(strings.ReplaceAll(strings.ToLower(mcHostRe.FindStringSubmatch(k)[1]), "_", "-"), "-")
		u, err := url.Parse(env[k])
		if err != nil || u.User == nil || u.Host == "" || !sources.NameRe.MatchString(name) {
			im.skip_("data source s3/%s: %s is not https://<access key>:<secret key>@<host>", name, k)
			continue
		}
		secretKey, _ := u.User.Password()
		if err := im.source(sources.Entry{Type: "s3", Name: name, Env: "shared", URL: u.Scheme + "://" + u.Host, Purpose: "backups",
			Username: u.User.Username()}, secretKey, im.envSrc[k]); err != nil {
			return err
		}
	}
	// Kubernetes: Settings -> Kubernetes' clusters and kubeconfigs
	if kd := filepath.Join(im.etc, "kube"); im.etc != "" {
		if fi, err := os.Stat(kd); err == nil && fi.IsDir() {
			if err := im.tree(kd, c.Values["ARGUS_KUBE_DIR"].Raw, "Kubernetes clusters"); err != nil {
				return err
			}
		}
	}
	// Claude
	if tok := env["CLAUDE_CODE_OAUTH_TOKEN"]; tok != "" {
		p := c.Values["ARGUS_CLAUDE_ENV_FILE"].Raw
		if !im.dry {
			s, err := im.key.Seal(secret.ClaudeName, tok)
			if err != nil {
				return err
			}
			if err := secret.WriteFile(p, []byte(claudetoken.Var+"="+s+"\n")); err != nil {
				return err
			}
		}
		im.did("Claude token: %s (sealed) -> %s", im.envSrc["CLAUDE_CODE_OAUTH_TOKEN"], p)
	}
	return nil
}

// --- carried settings: ARGUS_ETC_DIR/imported.env ----------------------------------------------------------------------

var tzKeyRe = regexp.MustCompile(`^([A-Z0-9]+_)?TZ$`)

// carriedValues are the non-secret site and integration keys of the env files (canonical names), with JIRA_PROJECT
// from jira.env and a time zone from a TZ-like key when ARGUS_TZ is not set.
func (im *importer) carriedValues() (map[string]string, map[string]string) {
	vals, from := map[string]string{}, map[string]string{}
	var ks []string
	for k := range im.env {
		ks = append(ks, k)
	}
	sort.Strings(ks) // canonical names first ("ARGUS_..." < "MATRIX_NOTIFY_ENABLED"): a canonical key beats an alias
	for _, k := range ks {
		ck := config.Canonical(k)
		if ck == "" || !config.IsImportable(ck) {
			continue
		}
		if _, ok := vals[ck]; ok && ck != k {
			continue
		}
		vals[ck], from[ck] = im.env[k], im.envSrc[k]+" "+k
	}
	if _, ok := vals["ARGUS_TZ"]; !ok {
		for _, k := range ks {
			if tzKeyRe.MatchString(k) && k != "ARGUS_TZ" {
				if _, err := time.LoadLocation(im.env[k]); err == nil && strings.Contains(im.env[k], "/") {
					vals["ARGUS_TZ"], from["ARGUS_TZ"] = im.env[k], im.envSrc[k]+" "+k
					break
				}
			}
		}
	}
	if _, ok := vals["JIRA_PROJECT"]; !ok && im.etc != "" {
		if b, err := os.ReadFile(filepath.Join(im.etc, "jira.env")); err == nil {
			if p := sources.ParseEnv(string(b))["JIRA_PROJECT"]; p != "" {
				vals["JIRA_PROJECT"], from["JIRA_PROJECT"] = p, "jira.env"
			}
		}
	}
	return vals, from
}

// carried writes the carried settings to ARGUS_ETC_DIR/imported.env and, when the GitLab issues connection's host is
// not the GITLAB_URL host and the earlier installation had no allowed-hosts file, the allowed hosts of both.
func (im *importer) carried(context.Context) error {
	vals, from := im.carriedValues()
	p := filepath.Join(im.c.EtcDir, config.ImportedFile)
	var ks []string
	for k := range vals {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if len(ks) == 0 {
		im.skip_("carried settings: the env files set none of the carried keys")
	} else {
		var b strings.Builder
		b.WriteString("# Written by argus import: settings carried from the earlier installation (no secret).\n")
		b.WriteString("# Read in ARGUS_LAYOUT=volume below argus.env and the environment; edit or delete freely.\n")
		for _, k := range ks {
			fmt.Fprintf(&b, "%s=%s\n", k, strings.ReplaceAll(vals[k], "\n", " "))
		}
		if !im.dry {
			if err := writeAtomic(p, []byte(b.String()), 0o640); err != nil {
				return err
			}
		}
		var names []string
		for _, k := range ks {
			names = append(names, k+" ("+strings.TrimSpace(from[k])+")")
		}
		im.did("carried settings: %d keys -> %s: %s", len(ks), p, strings.Join(names, ", "))
	}
	return im.gitlabHosts(vals["GITLAB_URL"])
}

// gitlabHosts writes gitlab-issues-allowed-hosts when the connection's host differs from the GITLAB_URL host (the
// guard's default) and there is no such file yet.
func (im *importer) gitlabHosts(gitlabURL string) error {
	if im.etc == "" || !fileOK(filepath.Join(im.etc, "gitlab-issues.json")) {
		return nil
	}
	dst := filepath.Join(im.c.EtcDir, "gitlab-issues-allowed-hosts")
	if fileOK(filepath.Join(im.etc, "gitlab-issues-allowed-hosts")) || fileOK(dst) && !im.force {
		return nil
	}
	var m struct {
		URL  string `json:"url"`
		Host string `json:"host"`
	}
	b, _ := os.ReadFile(filepath.Join(im.etc, "gitlab-issues.json"))
	json.Unmarshal(b, &m)
	var hosts []string
	for _, raw := range []string{gitlabURL, m.URL} {
		if h := hostKey(raw); h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	if h := strings.ToLower(strings.TrimSpace(m.Host)); h != "" && m.URL == "" && !slices.Contains(hosts, h) {
		hosts = append(hosts, h)
	}
	if len(hosts) < 2 && hostKey(gitlabURL) != "" {
		return nil // the connection is on the GITLAB_URL host: the guard's default allows it
	}
	if len(hosts) == 0 {
		im.skip_("GitLab issues allowed hosts: no GITLAB_URL and no host in gitlab-issues.json")
		return nil
	}
	if !im.dry {
		if err := writeAtomic(dst, []byte(strings.Join(hosts, "\n")+"\n"), 0o644); err != nil {
			return err
		}
	}
	im.did("GitLab issues allowed hosts: %s -> %s", strings.Join(hosts, ", "), dst)
	return nil
}

// hostKey is "host" or "host:port" (port when not 443) of an https URL; empty when it is not one.
func hostKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || !(u.Scheme == "https" || u.Scheme == "http") {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" && p != "443" {
		h += ":" + p
	}
	return h
}

func writeAtomic(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp := p + ".import.tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	os.Chmod(tmp, mode)
	return os.Rename(tmp, p)
}

// --- other databases ----------------------------------------------------------------------------------------------------

// otherDBs reports the databases at the top of the state folder that are not imported, with their size; googlebot.db
// (the crawler log database of the marketing module) is copied with -with-googlebot.
func (im *importer) otherDBs(ctx context.Context) error {
	m, _ := filepath.Glob(filepath.Join(im.src, "*.db"))
	sort.Strings(m)
	for _, p := range m {
		name := filepath.Base(p)
		if name == "ops.db" {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if name == "googlebot.db" && im.gbot {
			if err := im.copyDB(ctx, p, filepath.Join(im.c.StateDir, name), "crawler log database"); err != nil {
				return err
			}
			continue
		}
		hint := ""
		if name == "googlebot.db" {
			hint = "; -with-googlebot copies it"
		}
		im.skip_("%s: %s, not used by this version%s", name, humanSize(fi.Size()), hint)
	}
	return nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func fileOK(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// sealAll is argus secrets rekey over the volume: every plaintext secret sealed with the master key.
func (im *importer) sealAll(context.Context) error {
	if im.dry {
		im.did("secrets: every secret in the volume sealed with the master key (as argus secrets rekey)")
		return nil
	}
	r := rekeyFiles(secret.FilesOf(func(k string) string { return im.c.Values[k].Raw }), im.key, nil, false)
	if len(r.failed) > 0 {
		return fmt.Errorf("sealing: %d failed: %s", len(r.failed), strings.Join(r.failed, "; "))
	}
	im.did("secrets: %d sealed now, %d already sealed", r.sealed, r.current)
	return nil
}
