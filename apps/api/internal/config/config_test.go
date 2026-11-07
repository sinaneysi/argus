package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, file string, env ...string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "argus.env")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	if env == nil {
		env = []string{}
	}
	return Load(Options{File: path, Environ: env})
}

const minimal = "COMPANY_NAME=Acme\nARGUS_PUBLIC_URL=https://argus.acme.example\n"

func TestDefaults(t *testing.T) {
	c, err := load(t, minimal)
	if err != nil {
		t.Fatal(err)
	}
	if c.DB != "/var/lib/argus/ops.db" || c.BoardDB != "/var/lib/argus/board/board.db" || c.SecretsDir != "/etc/argus/secrets" {
		t.Fatalf("derived defaults: %q %q %q", c.DB, c.BoardDB, c.SecretsDir)
	}
	if c.Bind != "127.0.0.1:8088" || c.StaleHours != 25 || c.TZ != "UTC" || c.AdminUser != "admin" || c.DisplayName != "Argus" {
		t.Fatalf("defaults: %+v", c)
	}
	if got := c.Values["ARGUS_DB"].Source; got != "default" {
		t.Fatalf("source %q", got)
	}
	if len(c.TrustedProxies) != 2 || len(c.Warnings) != 0 {
		t.Fatalf("proxies %v warnings %v", c.TrustedProxies, c.Warnings)
	}
}

func TestEnvBeatsFileAndDefaultsFollowState(t *testing.T) {
	c, err := load(t, minimal+"ARGUS_STATE_DIR=/srv/argus\nARGUS_STALE_HOURS=12\n", "ARGUS_STALE_HOURS=48")
	if err != nil {
		t.Fatal(err)
	}
	if c.StaleHours != 48 || c.Values["ARGUS_STALE_HOURS"].Source != "env" {
		t.Fatalf("env must win: %d %q", c.StaleHours, c.Values["ARGUS_STALE_HOURS"].Source)
	}
	if c.DB != "/srv/argus/ops.db" || c.AlertsFile != "/srv/argus/alerts-firing.json" {
		t.Fatalf("defaults follow ARGUS_STATE_DIR: %q %q", c.DB, c.AlertsFile)
	}
}

func TestLegacyAliases(t *testing.T) {
	c, err := load(t, "", "COMPANY_NAME=Acme", "DEMO_MODE=1", "OPS_DB=/data/ops.db", "OPS_REPORT_BIND=0.0.0.0:8080",
		"OPS_DATA_DIR=/data", "ORG_NAME=Old", "ARGUS_AUTH_DB=/auth/new.db", "OPS_AUTH_DB=/auth/old.db")
	if err != nil {
		t.Fatal(err)
	}
	if c.DB != "/data/ops.db" || c.Bind != "0.0.0.0:8080" || c.StateDir != "/data" || c.AuthDB != "/auth/new.db" {
		t.Fatalf("aliases: %q %q %q %q", c.DB, c.Bind, c.StateDir, c.AuthDB)
	}
	if c.CompanyName != "Acme" {
		t.Fatalf("canonical COMPANY_NAME must beat ORG_NAME: %q", c.CompanyName)
	}
	if c.Values["ARGUS_DB"].Source != "env (OPS_DB)" {
		t.Fatalf("source %q", c.Values["ARGUS_DB"].Source)
	}
	w := strings.Join(c.Warnings, "\n")
	for _, want := range []string{"OPS_DB is deprecated, rename it to ARGUS_DB", "OPS_REPORT_BIND is deprecated",
		"OPS_DATA_DIR is deprecated", "ORG_NAME is ignored because COMPANY_NAME is set", "OPS_AUTH_DB is ignored"} {
		if !strings.Contains(w, want) {
			t.Errorf("missing warning %q in:\n%s", want, w)
		}
	}
	if c.BoardDB != "/data/board/board.db" {
		t.Fatalf("board db follows the aliased state dir: %q", c.BoardDB)
	}
}

func TestEveryProblemIsReported(t *testing.T) {
	file := "ARGUS_PUBLIC_URL=http://argus.example/x?y\nARGUS_DB=relative.db\nARGUS_STALE_HOURS=soon\n" +
		"ARGUS_BIND=nohost\nDEMO_MODE=yes\nARGUS_TZ=Mars/Olympus\nOIDC_ISSUER=https://sso.example\nARGUS_TYPO=1\n"
	c, err := load(t, file)
	errs, ok := err.(Errors)
	if !ok {
		t.Fatalf("want Errors, got %v", err)
	}
	all := errs.Error()
	for _, want := range []string{"COMPANY_NAME: required", "ARGUS_PUBLIC_URL: must start with https://", "ARGUS_DB: must be an absolute path",
		"ARGUS_STALE_HOURS: must be an integer", "ARGUS_BIND: must be host:port", "DEMO_MODE: must be 0 or 1",
		"ARGUS_TZ: unknown time zone", "OIDC_CLIENT_ID: required when single sign-on", "OIDC_CLIENT_SECRET: required"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if !strings.Contains(strings.Join(c.Warnings, "\n"), "unknown key ARGUS_TYPO") {
		t.Errorf("unknown ARGUS_ key must warn: %v", c.Warnings)
	}
	if c == nil || c.Values["ARGUS_DB"].Raw != "relative.db" {
		t.Fatal("the config must be returned with the errors")
	}
}

func TestURLRules(t *testing.T) {
	for raw, want := range map[string]string{
		"https://someone@argus.example/": "must not contain a user",
		"https://argus.example/?q=1":     "must not have a query",
		"https://":                       "has no host",
		"ftp://argus.example":            "must start with https://",
		"https://argus.example/":         "",
	} {
		if got := checkURL(raw); (want == "" && got != "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

func TestPublicURLOptionalInDemo(t *testing.T) {
	if _, err := load(t, "COMPANY_NAME=Northwind\nDEMO_MODE=1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := load(t, "COMPANY_NAME=Northwind\n"); err == nil || !strings.Contains(err.Error(), "ARGUS_PUBLIC_URL: required") {
		t.Fatalf("want ARGUS_PUBLIC_URL required, got %v", err)
	}
}

func TestParseEnvFile(t *testing.T) {
	m, errs := ParseEnvFile("argus.env", []byte("# comment\n\nA=1\n B = two words \nC=\"quoted # not a comment\"\nD='x'\nE=$HOME\nbad line\n1X=2\nA=3\n"))
	if m["A"] != "1" || m["B"] != "two words" || m["C"] != "quoted # not a comment" || m["D"] != "x" || m["E"] != "$HOME" {
		t.Fatalf("parsed %v", m)
	}
	all := errs.Error()
	for _, want := range []string{"argus.env:8: expected KEY=value", `argus.env:9: "1X" is not a valid key name`, "argus.env:10: A is already set on line 3"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
}

func TestMissingFile(t *testing.T) {
	// the default path may be missing; an explicit one may not
	if _, err := Load(Options{Environ: []string{"COMPANY_NAME=A", "DEMO_MODE=1", "ARGUS_CONFIG=/nonexistent/argus.env"}}); err == nil {
		t.Fatal("a missing ARGUS_CONFIG file must be an error")
	}
}

func TestPrintMasksSecrets(t *testing.T) {
	c, err := load(t, minimal+"ARGUS_ADMIN_INITIAL_PASSWORD=hunter2-very-secret\n", "OIDC_ISSUER=https://sso.example",
		"OIDC_CLIENT_ID=argus", "OIDC_CLIENT_SECRET=s3cr3t-value")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	c.Print(&b)
	out := b.String()
	if strings.Contains(out, "hunter2") || strings.Contains(out, "s3cr3t") {
		t.Fatalf("secret printed:\n%s", out)
	}
	for _, want := range []string{"ARGUS_ADMIN_INITIAL_PASSWORD=******", "OIDC_CLIENT_SECRET=******", "# file", "# env",
		"ARGUS_DB=/var/lib/argus/ops.db", "DEMO_PASSWORD=", "# not set"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestExampleFileIsValid(t *testing.T) {
	data, err := os.ReadFile("../../config/argus.example.env")
	if err != nil {
		t.Fatal(err)
	}
	m, errs := ParseEnvFile("argus.example.env", data)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	known := map[string]bool{}
	for _, f := range Fields {
		known[f.Key] = true
	}
	for k := range m {
		if !known[k] {
			t.Errorf("example sets %s, which the loader does not know", k)
		}
	}
	if _, err := load(t, string(data)); err != nil {
		t.Fatalf("the example must pass config check: %v", err)
	}
}
