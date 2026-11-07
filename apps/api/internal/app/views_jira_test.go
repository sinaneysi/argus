package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

const jiraTestToken = "SECRETtoken0123456789abcdefXYZ"

func jiraReadDrop(t *testing.T, dir, user string) (map[string]any, os.FileInfo) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var files []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), user+"-") && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e.Name())
		}
	}
	if len(files) != 1 {
		t.Fatalf("want one request, got %v", files)
	}
	p := filepath.Join(dir, files[0])
	st, _ := os.Stat(p)
	data, _ := os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	os.Remove(p)
	return m, st
}

func TestJiraConnectDropsTheTokenOnceForRootOnly(t *testing.T) {
	e := boardSetup(t)
	code, loc := JiraPostAccount(e.ctx(e.view), boardForm("action", "connect", "url", " https://jira.example.com ", "token", jiraTestToken, "mode", "rw"), e.view)
	if code != 303 || !strings.HasPrefix(loc, "/account?jira=") || !strings.HasSuffix(loc, "#jira") {
		t.Fatal(code, loc)
	}
	m, st := jiraReadDrop(t, filepath.Join(SET_IN, "jira-connect"), "viewer1")
	if st.Mode().Perm() != 0o600 {
		t.Fatal("mode", st.Mode())
	}
	if m["token"] != jiraTestToken || m["url"] != "https://jira.example.com" || m["user"] != "viewer1" || m["kind"] != "connect" {
		t.Fatal(m)
	}
	// the token never comes back in a page, refused or not
	for _, form := range []py.QS{
		boardForm("action", "connect", "url", "http://jira.example.com", "token", jiraTestToken),
		boardForm("action", "connect", "url", "https://jira.example.com", "token", jiraTestToken, "mode", "admin"),
		boardForm("action", "connect", "url", "https://jira.example.com", "token", jiraTestToken+"<"),
	} {
		_, page := JiraPostAccount(e.ctx(e.view), form, e.view)
		if strings.Contains(page, jiraTestToken) || page == "" {
			t.Fatal("token echoed or no answer")
		}
	}

}

func TestJiraHostsAndSharedValidation(t *testing.T) {
	e := boardSetup(t)
	if c, _ := JiraPostHosts(e.ctx(e.view), boardForm("hosts", "a.b"), e.view); c != 403 {
		t.Fatal("viewer")
	}
	c, page := JiraPostHosts(e.ctx(e.admin), boardForm("hosts", "jira.example.com\n<script>"), e.admin)
	if c != 400 || !strings.Contains(page, "Not a host name") {
		t.Fatal("bad host", c)
	}
	c, loc := JiraPostHosts(e.ctx(e.admin), boardForm("hosts", " Jira.Example.com , other.host:8443"), e.admin)
	if c != 303 || !strings.HasSuffix(loc, "#hosts") {
		t.Fatal(c, loc)
	}
	m, _ := jiraReadDrop(t, filepath.Join(SET_IN, "jira-connect"), "alex.doe")
	if hs, _ := json.Marshal(m["hosts"]); string(hs) != `["jira.example.com","other.host:8443"]` {
		t.Fatal(string(hs))
	}
	for _, f := range []py.QS{boardForm("action", "shared-settings", "project", "abc!"), boardForm("action", "shared-settings", "board_id", "ftp://x"),
		boardForm("action", "shared-settings", "mode", "x")} {
		if c, _ := JiraPostShared(e.ctx(e.admin), f, e.admin); c != 400 {
			t.Fatal("shared validation", f)
		}
	}
	if c, _ := JiraPostShared(e.ctx(e.view), boardForm("action", "shared-disconnect"), e.view); c != 403 {
		t.Fatal("shared viewer")
	}
	c, _ = JiraPostShared(e.ctx(e.admin), boardForm("action", "shared-connect", "url", "https://j.example", "token", jiraTestToken,
		"project", "dev", "ca_pem", "-----BEGIN\r\nX"), e.admin)
	if c != 303 {
		t.Fatal(c)
	}
	m, _ = jiraReadDrop(t, filepath.Join(SET_IN, "jira-connect"), "alex.doe")
	if m["project"] != "DEV" || m["ca_pem"] != "-----BEGIN\nX" || m["kind"] != "shared-connect" {
		t.Fatal(m)
	}
}
