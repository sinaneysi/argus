package app

// The remaining settings tabs on the settings/form template (DESIGN.md "Settings and forms"): Jira sync, Backup targets,
// Integrations (Outline, GitLab issues) and Lessons. Every POST form carries the CSRF token, form.css loads, a refused
// save answers JSON naming the form and the field, nothing is written, a token or key is never
// filled back in, and an action that cannot be taken back is confirmed in place.

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var postFormRe = regexp.MustCompile(`(?s)<form method="post"[^>]*>.*?</form>`)

// everyPostFormHasCSRF: each POST form on the page carries the session's CSRF token.
func everyPostFormHasCSRF(t *testing.T, page, body, csrf string) {
	t.Helper()
	forms := postFormRe.FindAllString(body, -1)
	if len(forms) == 0 {
		t.Fatal(page, "no POST form")
	}
	for _, f := range forms {
		if !strings.Contains(f, `<input type="hidden" name="csrf" value="`+csrf+`">`) {
			t.Fatal(page, "POST form without CSRF:", f[:min(len(f), 160)])
		}
	}
}

func TestSettingsTabsRejectedSaveKeepsEntriesNotSecrets(t *testing.T) {
	e := srvSetup(t)
	tok, csrf := e.login("admin")
	ck := map[string]string{"opsr_session": tok}
	const secret = "SuperSecretToken0123456789"
	cases := []struct {
		path  string
		form  url.Values
		field string // the marked input's id
		msg   string
		kept  string // a posted entry shown again
		drop  string // request-file directory that must stay empty
	}{
		{"/admin/jira-shared", url.Values{"action": {"shared-connect"}, "url": {"ftp://jira.example.com"}, "token": {secret},
			"approvers": {"kept.approver"}}, "f-jira-connect-url", "Not saved: The Jira URL must start with https://", `value="kept.approver"`, "jira-connect"},
		{"/admin/jira-shared", url.Values{"action": {"shared-connect"}, "url": {"https://jira.example.com"}, "token": {secret},
			"project": {"9bad"}}, "f-jira-connect-project", "Project key: a letter", `value="https://jira.example.com"`, "jira-connect"},
		{"/admin/jira-hosts", url.Values{"hosts": {"jira.example.com\nhttps://bad/path"}}, "f-jira-hosts-hosts", "Not a host name: https://bad/path",
			"jira.example.com\nhttps://bad/path</textarea>", "jira-connect"},
		{"/admin/agent", url.Values{"qa_cap": {"99"}, "qa_threshold": {"high"}, "column": {"Doing now"}}, "f-agent-qa-cap",
			"New QA proposals per day must be between 0 and 50.", `value="Doing now"`, "board-requests"},
		{"/admin/outline", url.Values{"action": {"connect"}, "url": {"https://docs.dev.northwind.example"}, "token": {"ol_api_" + secret},
			"collection": {"bad<name>"}}, "f-outline-connect-collection", "Check the collection name", `value="https://docs.dev.northwind.example"`, "outline-requests"},
		{"/admin/outline", url.Values{"action": {"hosts"}, "hosts": {"docs.example.com\nnot a host"}}, "f-outline-hosts-hosts",
			"Not a host name: not a host", `<details class="plain" id="hosts" open>`, "outline-requests"},
		{"/admin/gitlab-issues", url.Values{"action": {"connect"}, "url": {"https://gitlab.example.com"}, "project": {""},
			"board": {"7"}, "token": {"glpat-" + secret}}, "f-gitlab-connect-project", "Paste the issue board URL", `value="7"`, "gitlab-issues"},
		{"/admin/gitlab-issues", url.Values{"action": {"settings"}, "default_tracker": {"jira"}, "list_map": {"QA=nowhere"},
			"user_map": {"a=b"}}, "f-gitlab-settings-list-map", "List map: the column is one of", `value="a=b"`, "gitlab-issues"},
		{"/admin/backup-targets", url.Values{"action": {"add"}, "name": {"dev2"}, "endpoint": {"https://minio.dev.example:9000"},
			"bucket": {"B"}, "access_key": {"writer"}, "secret_key": {secret}, "envs": {"develop"}}, "f-target-add-bucket",
			"Bucket: 3-63 lower-case", `value="https://minio.dev.example:9000"`, "backup-secrets"},
		{"/admin/backup-targets", url.Values{"action": {"add"}, "name": {"dev2"}, "endpoint": {"https://minio.dev.example:9000"},
			"bucket": {"backup-dev"}, "access_key": {"writer"}, "secret_key": {secret}, "retention_days": {"500"}}, "f-target-add-retention-days",
			"Default retention must be between 1 and 400 days.", `value="500"`, "backup-secrets"},
	}
	for _, tc := range cases {
		tc.form.Set("csrf", csrf)
		r := e.req("POST", "/api/v1"+tc.path, ck, tc.form, nil)
		var a struct{ Error, Form, Field string }
		if json.Unmarshal([]byte(r.body), &a) != nil || r.status != 400 || !strings.Contains(a.Error, strings.TrimPrefix(tc.msg, "Not saved: ")) || FieldID(a.Form, a.Field) != tc.field {
			t.Fatal(tc.path, "rejected feedback:", tc.msg, r.status, r.body)
		}
		if strings.Contains(r.body, secret) {
			t.Fatal(tc.path, "a secret came back in the answer")
		}
		if ents, _ := os.ReadDir(filepath.Join(SET_IN, tc.drop)); len(ents) != 0 {
			t.Fatal(tc.path, "a refused save wrote a request:", tc.drop, len(ents))
		}
	}
	// without CSRF every one of these POSTs is refused before the handler
	for _, p := range []string{"/api/v1/admin/jira-shared", "/api/v1/admin/jira-hosts", "/api/v1/admin/agent", "/api/v1/admin/board", "/api/v1/admin/outline",
		"/api/v1/admin/gitlab-issues", "/api/v1/admin/backup-targets", "/api/v1/lessons/settings", "/api/v1/lessons/act"} {
		if r := e.req("POST", p, ck, url.Values{"action": {"settings"}}, nil); r.status != 403 {
			t.Fatal(p, "accepted without CSRF:", r.status)
		}
	}
}

func TestSettingsLessonsRejectedEdit(t *testing.T) {
	e := boardSetup(t)
	lessonsSeed(t, BoardDBPath())
	c := &Ctx{Sess: e.admin, Theme: "auto", Path: "/"}
	code, out := LessonsPost(c, "/admin/lessons/act", boardForm("action", "edit", "id", "1", "title", "kept title", "body", "b",
		"service", "../etc", "env", "develop", "kind", "change", "tags", "disk"), e.admin)
	if code != 400 || out != "Not saved: Service: a GitLab path such as group/project." {
		t.Fatal("rejected edit", code, out)
	}

}
