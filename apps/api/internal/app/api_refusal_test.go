package app

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

var reHTMLTag = regexp.MustCompile(`</?(h1|h2|p|div|a|span|form|html)\b`)

// Every write answers JSON on every refusal path: never a page, never HTML in the message. The sweep posts each
// /api/v1 write as an admin and as a viewer, with an empty body and with an unknown action.
func TestEveryWriteAnswersJSONNeverHTML(t *testing.T) {
	srv := srvSetup(t)
	adm, _ := srv.login("admin")
	vw, _ := srv.login("view.er")
	n := 0
	for p := range POST_ROUTES {
		if !strings.HasPrefix(p, "/api/v1/") || !apiIsPost(p) {
			continue
		}
		n++
		for _, tok := range []string{adm, vw} {
			for _, body := range []map[string]any{{}, {"action": "no-such-action", "id": "1", "key": "x"}} {
				r := srv.v1Post(p, map[string]string{"opsr_session": tok}, body, nil)
				if !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
					t.Fatalf("%s: answered %s", p, r.header.Get("Content-Type"))
				}
				var m map[string]any
				if err := json.Unmarshal([]byte(r.body), &m); err != nil {
					t.Fatalf("%s: not JSON: %.80s", p, r.body)
				}
				if msg, _ := m["error"].(string); reHTMLTag.MatchString(msg) || (r.status >= 400 && msg == "") {
					t.Errorf("%s %d: error %.120q", p, r.status, msg)
				}
			}
		}
	}
	if n < 50 {
		t.Fatal("only", n, "write routes swept")
	}
}

// There is no POST route outside /api/: the old form routes answer 404 and write nothing.
func TestNoPostRouteOutsideTheAPI(t *testing.T) {
	e := srvSetup(t)
	tok, csrf := e.login("admin")
	ck := map[string]string{"opsr_session": tok}
	for _, p := range []string{"/login", "/login/2fa", "/logout", "/theme", "/scan", "/board/move", "/admin", "/admin/users", "/account",
		"/account/2fa/begin", "/incidents/action", "/backups/override", "/vulns/suppress", "/admin/kube"} {
		r := e.req("POST", p, ck, map[string][]string{"csrf": {csrf}, "action": {"add"}, "tasks": {"all"}}, nil)
		if r.status != 404 || !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
			t.Errorf("%s: %d", p, r.status)
		}
	}
	if auth.GetSession(tok) == nil {
		t.Fatal("a POST outside the API ended the session")
	}
}
