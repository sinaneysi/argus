package config

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// webKeysBefore is every environment name argus-api read itself before it used this loader (main 8e76a6a).
// Each must still configure the web app: as a canonical key, or as an alias of one.
var webKeysBefore = []string{
	"ARGUS_HOSTNAME", "ARGUS_REPO", "ARGUS_SSH_ALIAS", "ARGUS_TZ", "ARGUS_TZ_LABEL", "BACKUP_TEMPLATES_REPO",
	"CICD_ENGINE_PROJECT", "CICD_ENGINE_PROJECT_ID", "CICD_GROUPS", "CICD_STUCK_MINUTES", "COMPANY_NAME",
	"DEMO_ADMIN", "DEMO_BANNER", "DEMO_MODE", "DEMO_PASSWORD", "DEMO_VIEWER", "ENV_LABELS", "FEATURE_PROBLEMS",
	"FEATURE_TODAY", "GITLAB_URL", "JIRA_PROJECT", "MATRIX_QA_ROOM_LABEL", "MATRIX_SERVER_NAME",
	"MFA_REQUIRED_FOR_ACTIONS", "MFA_REQUIRED_FOR_LOGIN", "MFA_SENSITIVE_MINUTES", "MFA_STEPUP_MINUTES",
	"MFA_STEP_UP", "MFA_TRUST_IDP", "OBS_SITES", "OBS_SITE_EAST_LABEL", "OIDC_CA_FILE", "OIDC_CLIENT_ID",
	"OIDC_CLIENT_SECRET", "OIDC_ISSUER", "OIDC_MFA_ACR", "OIDC_MFA_AMR", "OPS_ADMINS", "OPS_ADMIN_INITIAL_PASSWORD",
	"OPS_ADMIN_USER", "OPS_ALERTS", "OPS_AUTH_DB", "OPS_BACKUP_PLANS", "OPS_BACKUP_REF", "OPS_BOARD_DB",
	"OPS_CRED_EXPIRY", "OPS_CRED_STATUS", "OPS_DATA_DIR", "OPS_DB", "OPS_DEBUG_SQL", "OPS_IMPORT_HTPASSWD",
	"OPS_NO_QUERY_CACHE", "OPS_PUBLIC_URL", "OPS_READINESS_DATA", "OPS_REPORTS_DIR", "OPS_REPORT_BIND",
	"OPS_SECRET_IN", "OPS_SELFCHECK", "OPS_SETTINGS_IN", "OPS_SETTINGS_OUT", "OPS_STALE_HOURS", "OPS_STATIC",
	"OPS_TRUSTED_PROXIES", "ORG_NAME", "OUTLINE_URL", "PUBLIC_DOMAIN", "SESSION_IDLE_HOURS", "SESSION_MAX_HOURS",
	"SESSION_REMEMBER_ABSOLUTE_DAYS", "SESSION_REMEMBER_DAYS", "SESSION_REMEMBER_IDLE_DAYS", "TOTP_KEY_FILE",
	"TOTP_KEY_FILE_OLD", "TOTP_LEGACY_LABEL",
}

func emptyFile(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "argus.env")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// canonicalOf is the canonical key an old or current name configures ("" when unknown).
func canonicalOf(name string) string {
	if f := fieldFor(name); f != nil {
		return name
	}
	for _, f := range Fields {
		for _, a := range f.Aliases {
			if a == name {
				return f.Key
			}
		}
	}
	return ""
}

func TestEveryOldWebKeyStillWorks(t *testing.T) {
	for _, k := range webKeysBefore {
		canon := canonicalOf(k)
		if canon == "" {
			t.Errorf("%s: the web app read it before; the loader must know it", k)
			continue
		}
		val := "/srv/x"
		switch fieldFor(canon).Kind {
		case Int:
			val = "5"
		case Bool:
			val = "1"
		case URL:
			val = "https://h.example"
		case Addr:
			val = "127.0.0.1:9"
		case Zone:
			val = "Europe/Paris"
		}
		c, _ := Load(Options{File: emptyFile(t), Environ: []string{k + "=" + val}})
		v := c.Values[canon]
		if v.Raw != val {
			t.Errorf("%s -> %s: got %q", k, canon, v.Raw)
		}
		if k != canon {
			if v.Source != "env ("+k+")" || !strings.Contains(strings.Join(c.Warnings, "\n"), k+" is deprecated, rename it to "+canon) {
				t.Errorf("%s: source %q warnings %v", k, v.Source, c.Warnings)
			}
		}
	}
}

func TestAliasPrecedence(t *testing.T) {
	// env canonical > env alias > file canonical > file alias > default
	cases := []struct {
		file string
		env  []string
		want string
		src  string
	}{
		{"", nil, "/var/lib/argus/alerts-firing.json", "default"},
		{"OPS_ALERTS=/f/old.json\n", nil, "/f/old.json", "file (OPS_ALERTS)"},
		{"OPS_ALERTS=/f/old.json\nARGUS_ALERTS_FILE=/f/new.json\n", nil, "/f/new.json", "file"},
		{"ARGUS_ALERTS_FILE=/f/new.json\n", []string{"OPS_ALERTS=/e/old.json"}, "/e/old.json", "env (OPS_ALERTS)"},
		{"ARGUS_ALERTS_FILE=/f/new.json\n", []string{"OPS_ALERTS=/e/old.json", "ARGUS_ALERTS_FILE=/e/new.json"}, "/e/new.json", "env"},
		{"", []string{"OPS_DATA_DIR=/data"}, "/data/alerts-firing.json", "default"},
	}
	for i, tc := range cases {
		c, _ := load(t, minimal+tc.file, tc.env...)
		if v := c.Values["ARGUS_ALERTS_FILE"]; v.Raw != tc.want || v.Source != tc.src {
			t.Errorf("case %d: %q from %q, want %q from %q", i, v.Raw, v.Source, tc.want, tc.src)
		}
	}
	c, _ := load(t, minimal, "OPS_ADMIN_USER=old.admin", "ARGUS_ADMIN_USER=new.admin")
	if c.AdminUser != "new.admin" || !strings.Contains(strings.Join(c.Warnings, "\n"), "OPS_ADMIN_USER is ignored because ARGUS_ADMIN_USER is set") {
		t.Fatalf("both set: %q %v", c.AdminUser, c.Warnings)
	}
}

func TestPatternKeysAndRanges(t *testing.T) {
	c, err := load(t, minimal+"OBS_SITE_WEST_LABEL=from-file\n", "OBS_SITE_EAST_LABEL=dc-east", "OBS_SITE_WEST_LABEL=dc-west")
	if err != nil {
		t.Fatal(err)
	}
	if c.Values["OBS_SITE_EAST_LABEL"].Raw != "dc-east" || c.Values["OBS_SITE_WEST_LABEL"].Raw != "dc-west" {
		t.Fatalf("pattern keys: %v", c.Values)
	}
	var b bytes.Buffer
	c.Print(&b)
	if !strings.Contains(b.String(), "OBS_SITE_EAST_LABEL=dc-east") || strings.Contains(b.String(), "OBS_SITE_*_LABEL") {
		t.Fatalf("print:\n%s", b.String())
	}
	_, err = load(t, minimal+"SESSION_MAX_HOURS=0\nMFA_SENSITIVE_MINUTES=9999\n")
	if err == nil || !strings.Contains(err.Error(), "SESSION_MAX_HOURS: must be between 1 and 744") ||
		!strings.Contains(err.Error(), "MFA_SENSITIVE_MINUTES: must be between 1 and 1440") {
		t.Fatalf("ranges: %v", err)
	}
	if c.BoardDB != "/var/lib/argus/board/board.db" {
		t.Fatalf("board db next to ARGUS_DB: %q", c.BoardDB)
	}
}

func TestEntriesMarkAliasesAndMaskSecrets(t *testing.T) {
	c, _ := load(t, minimal, "OPS_DB=/srv/ops.db", "DEMO_PASSWORD=very-secret-demo")
	got := map[string]Entry{}
	for _, e := range c.Entries() {
		got[e.Key] = e
	}
	if e := got["ARGUS_DB"]; e.Source != "alias" || e.Alias != "OPS_DB" || e.Value != "/srv/ops.db" {
		t.Fatalf("alias entry %+v", e)
	}
	if e := got["DEMO_PASSWORD"]; e.Value != "******" || !e.Secret {
		t.Fatalf("secret entry %+v", e)
	}
	if e := got["ARGUS_HOSTNAME"]; e.Source != "unset" {
		t.Fatalf("unset entry %+v", e)
	}
}

func TestSchemaFileIsCurrent(t *testing.T) {
	data, err := os.ReadFile("../../config/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, SchemaJSON()) {
		t.Fatal("config/schema.json is stale: run  go run ./cmd/argus config schema > config/schema.json")
	}
}

// TestDemoEnvIsValid: the demo image's ENV plus demo/compose.yml's environment must pass the loader with no
// deprecation warnings (the demo uses the canonical names).
func TestDemoEnvIsValid(t *testing.T) {
	env := demoImageEnv(t)
	if len(env) < 10 {
		t.Fatalf("demo/Dockerfile ENV not found: %v", env)
	}
	c, err := Load(Options{File: emptyFile(t), Environ: append(env, demoComposeEnv(t)...)})
	if err != nil {
		t.Fatalf("the demo configuration must be valid: %v", err)
	}
	if len(c.Warnings) > 0 {
		t.Fatalf("the demo must use canonical names: %v", c.Warnings)
	}
	if c.StateDir != "/data" || c.AlertsFile != "/data/alerts-firing.json" || c.Bind != "0.0.0.0:8080" || !c.DemoMode {
		t.Fatalf("demo values: %q %q %q %v", c.StateDir, c.AlertsFile, c.Bind, c.DemoMode)
	}
}

func demoImageEnv(t *testing.T) []string {
	data, err := os.ReadFile("../../../../demo/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	// the app stage's one ENV instruction, with its continuation lines
	s := string(data)
	i := strings.Index(s, "\nENV DEMO_MODE=")
	if i < 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s[i+1:], "\n") {
		cont := strings.HasSuffix(strings.TrimSpace(line), "\\")
		for _, w := range strings.Fields(strings.TrimSuffix(strings.TrimSpace(line), "\\")) {
			if w != "ENV" && strings.Contains(w, "=") {
				out = append(out, w)
			}
		}
		if !cont {
			break
		}
	}
	return out
}

func demoComposeEnv(t *testing.T) []string {
	f, err := os.Open("../../../../demo/compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	kv := regexp.MustCompile(`^      ([A-Z][A-Z0-9_]*): *(.*?) *(#.*)?$`)
	var out []string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.TrimSpace(line) == "environment:":
			in = true
		case in && kv.MatchString(line):
			m := kv.FindStringSubmatch(line)
			out = append(out, m[1]+"="+strings.Trim(m[2], `"`))
		case in && !strings.HasPrefix(line, "      "):
			in = false
		}
	}
	return out
}

// TestWebAppReadsOnlyThroughTheLoader: argus-api and its packages never read the environment themselves, and every
// key they name exists in the schema. A new os.Getenv in the web app fails here.
func TestWebAppReadsOnlyThroughTheLoader(t *testing.T) {
	raw := regexp.MustCompile(`\b(os\.(Getenv|LookupEnv|Environ)|syscall\.(Getenv|Environ))\(`)
	named := regexp.MustCompile(`\b(env|envOr|envInt|envSet|setting|config\.Get|config\.Lookup|get|pick\([^,]+,)\s*\(?"([A-Z][A-Z0-9_]*)"`)
	seen := 0
	for _, dir := range []string{"../../cmd/argus-api", "../app", "../auth", "../oidc", "../brand", "../py"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for n, line := range strings.Split(string(data), "\n") {
				if raw.MatchString(line) {
					t.Errorf("%s:%d reads the environment directly; add the key to config.Fields and use the loader: %s", path, n+1, strings.TrimSpace(line))
				}
				for _, m := range named.FindAllStringSubmatch(line, -1) {
					seen++
					if fieldFor(m[2]) == nil {
						t.Errorf("%s:%d names %s, which is not a canonical key of config.Fields", path, n+1, m[2])
					}
				}
			}
		}
	}
	if seen < 50 {
		t.Fatalf("found only %d named settings; the scan pattern is broken", seen)
	}
}
