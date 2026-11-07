package app

// Settings → Credentials (/admin/credentials) and the Overview card on a credentials.json fixture: admins only (viewer
// 403, anonymous to login), the empty state, the attention table with days-left badges whose word matches the colour,
// healthy rows folded, not-configured folded, How to rotate per kind, escaping of every field, no secret-looking field
// is ever rendered (the page renders a fixed set of metadata keys), the Check now form (CSRF, admins only, the request
// file) and its busy state, the Settings tab, and the Overview card.

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func credFixture(t *testing.T, e *srvEnv) {
	t.Helper()
	now := I2S(NowInt())
	doc := `{"v": 1, "checked_at": ` + now + `, "thresholds": {"warn_days": 30, "crit_days": 7, "rotate_days": 180, "today_days": 14},
 "attention": 3, "worst": "FAIL", "next": {"id": "ci-vars-meta", "name": "GitLab CI variable metadata", "expires_at": "2026-10-29", "days_left": 5},
 "certificates": {"link": "/certs"},
 "items": [
  {"id": "backup-templates", "name": "GitLab backup plans", "kind_label": "GitLab project access token", "used_by": "New backup",
   "where": "/etc/argus/backup-templates.token", "status": "rejected", "level": "FAIL", "band": "unknown", "days_left": null,
   "expires_at": null, "detail": "rejected by the service (401): revoked, expired or wrong", "checked_at": ` + now + `,
   "howto": ["Rotate it in GitLab.", "Store it in /etc/argus/backup-templates.token"], "value": "glpat-SHOULDNEVERRENDER", "_fixture": "secret-scan: allow"},
  {"id": "ci-vars-meta", "name": "GitLab CI variable metadata", "kind_label": "GitLab group access token", "used_by": "ci-vars-meta",
   "where": "/etc/argus/ci-vars-meta.token", "status": "crit", "level": "WARN", "band": "crit", "days_left": 5,
   "expires_at": "2026-10-29", "expiry_source": "api", "identity": "<script>alert(1)</script>", "created_at": ` + now + `,
   "created_source": "api", "last_used": ` + now + `, "detail": "expires 2026-10-29 (5 d)", "checked_at": ` + now + `,
   "howto": ["POST /api/v4/groups/northwind/access_tokens/380/rotate <b>b</b>"]},
  {"id": "nvd", "name": "NVD API key", "kind_label": "API key", "used_by": "CVE feeds", "where": "/etc/argus/nvd-api-key",
   "status": "rotate", "level": "WARN", "band": "unknown", "days_left": null, "expires_at": null, "rotate_due": "2026-09-01",
   "rotate_days": 180, "detail": "no expiry info; set 200 d ago, rotate by 2026-09-01 (overdue)", "checked_at": ` + now + `, "howto": ["Request a key."]},
  {"id": "gitlab-ro", "name": "GitLab read-only", "kind_label": "GitLab impersonation token", "used_by": "gitlab-ro.sh",
   "where": "/etc/argus/env (GITLAB_RO_TOKEN)", "status": "ok", "level": "OK", "band": "ok", "days_left": 358,
   "expires_at": "2027-10-17", "detail": "expires 2027-10-17 (358 d)", "checked_at": ` + now + `, "howto": ["Make a new one."]},
  {"id": "jira-shared", "name": "Jira shared", "kind_label": "Jira personal access token", "used_by": "board sync",
   "where": "/etc/argus/jira.pat", "status": "ok", "level": "OK", "band": "never", "expiry_source": "never", "days_left": null,
   "rotate_due": "2027-04-18", "rotate_days": 180, "detail": "never expires", "checked_at": ` + now + `, "howto": ["Jira steps."]},
  {"id": "outline", "name": "Outline API key", "kind_label": "Outline API key", "used_by": "Outline docs", "where": "/etc/argus/outline.token",
   "status": "absent", "level": "OK", "band": "absent", "detail": "not configured", "checked_at": ` + now + `, "howto": []}
 ]}`
	if err := os.WriteFile(filepath.Join(SET_OUT, "credentials.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialsCheckNow(t *testing.T) {
	e := srvSetup(t)
	credFixture(t, e)
	tok, csrf := e.login("admin")
	ck := map[string]string{auth.COOKIE: tok}
	if r := e.req("POST", "/api/v1/scan", ck, url.Values{"csrf": {"x" + csrf[1:]}, "tasks": {"cred-expiry"}}, nil); r.status != 403 {
		t.Fatal("bad csrf", r.status)
	}
	req := filepath.Join(SET_IN, "requests", "cred-expiry.req")
	if _, err := os.Stat(req); err == nil {
		t.Fatal("request without a valid token")
	}
	vt, vcsrf := e.login("view.er")
	e.req("POST", "/api/v1/scan", map[string]string{auth.COOKIE: vt}, url.Values{"csrf": {vcsrf}, "tasks": {"cred-expiry"}, "back": {"/"}}, nil)
	if _, err := os.Stat(req); err == nil {
		t.Fatal("a viewer queued the credential check")
	}
	r := e.req("POST", "/api/v1/scan", ck, url.Values{"csrf": {csrf}, "tasks": {"cred-expiry"}, "back": {"/admin/credentials"}}, nil)
	if r.status != 200 || r.loc != "/admin/credentials" {
		t.Fatal("redirect", r.status, r.loc)
	}
	data, err := os.ReadFile(req)
	if err != nil || !strings.Contains(string(data), `"by": "admin"`) {
		t.Fatalf("request %q %v", data, err)
	}

}
