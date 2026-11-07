package app

// The full lists of Review as JSON: ideas (any signed-in user reads, admins decide) and proposed tasks (admins only). Writes
// pass CSRF, role, 2FA and the demo refusal, and drop the same request files as the Go pages.

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1Ideas(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	if r := e.req("GET", "/api/v1/ideas", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	d := v1JSON(t, e.req("GET", "/api/v1/ideas?status=all", ck(adm), nil, nil))
	rows := d["rows"].([]any)
	if len(rows) == 0 || len(d["tabs"].([]any)) != 7 || len(d["facets"].([]any)) != 3 || len(d["advisers"].([]any)) == 0 || d["list"].(map[string]any)["path"] != "/review" {
		t.Fatal(v1Str(d))
	}
	if strings.Contains(v1Str(d), "<a ") || !strings.Contains(d["tabs"].([]any)[0].(map[string]any)["href"].(string), "kind=ideas&mode=full") {
		t.Fatal(v1Str(d))
	}
	if len(rows[0].(map[string]any)["actions"].([]any)) == 0 {
		t.Fatal(rows[0])
	}
	v := v1JSON(t, e.req("GET", "/api/v1/ideas?status=all", ck(vw), nil, nil))
	for _, x := range v["rows"].([]any) {
		if len(x.(map[string]any)["actions"].([]any)) != 0 {
			t.Fatal(x)
		}
	}
	if v["advisers"].([]any)[0].(map[string]any)["can"] != false {
		t.Fatal(v1Str(v))
	}
	if r := e.req("POST", "/api/v1/ideas/act", ck(adm), url.Values{"x": {"1"}}, nil); r.status != 403 {
		t.Fatal("no csrf", r.status)
	}
	id := I2S(int64(rows[0].(map[string]any)["id"].(float64)))
	if r := e.v1Post("/api/v1/ideas/act", ck(vw), map[string]any{"id": id, "action": "explore"}, nil); r.status != 403 {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/ideas/act", ck(adm), map[string]any{"id": id, "action": "reject", "reason": "x"}, nil); r.status != 400 || !strings.Contains(r.body, "reason") {
		t.Fatal(r.status, r.body)
	}
	r := e.v1Post("/api/v1/ideas/act", ck(adm), map[string]any{"id": id, "action": "explore"}, nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.Contains(m["redirect"].(string), "sent=explore") {
		t.Fatal(r.status, r.body)
	}
	if l, _ := filepath.Glob(filepath.Join(SET_IN, "idea-actions", "*.json")); len(l) != 1 {
		t.Fatal(l)
	}
	p := d["advisers"].([]any)[0].(map[string]any)["key"].(string)
	if r := e.v1Post("/api/v1/ideas/generate", ck(adm), map[string]any{"persona": "nobody"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/ideas/generate", ck(adm), map[string]any{"persona": p}, nil); r.status != 200 {
		t.Fatal(r.status, r.body)
	}
	if l, _ := filepath.Glob(filepath.Join(SET_IN, "requests", "ideas-"+p+".req")); len(l) != 1 {
		t.Fatal(l)
	}
}

func TestAPIv1Proposals(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	if r := e.req("GET", "/api/v1/proposals", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := e.req("GET", "/api/v1/proposals", ck(vw), nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	d := v1JSON(t, e.req("GET", "/api/v1/proposals", ck(adm), nil, nil))
	rows := d["rows"].([]any)
	if len(rows) == 0 || len(d["tiles"].([]any)) != 4 || len(d["tabs"].([]any)) != 3 || d["accept"] == nil || len(d["chips"].([]any)) == 0 || d["settings"] == nil {
		t.Fatal(v1Str(d))
	}
	if strings.Contains(v1Str(d), "<a ") || strings.Contains(v1Str(d), "<span") {
		t.Fatal("HTML in the API", v1Str(d))
	}
	if f := v1JSON(t, e.req("GET", "/api/v1/proposals?sev=critical&sev=high", ck(adm), nil, nil)); !strings.Contains(f["here"].(string), "sev=critical&sev=high") || f["filtered"] != true {
		t.Fatal(f["here"], f["filtered"])
	}
	if f := v1JSON(t, e.req("GET", "/api/v1/proposals?view=decided", ck(adm), nil, nil)); len(f["decided"].([]any)) == 0 || f["view"] != "decided" {
		t.Fatal(v1Str(f))
	}
	id := I2S(int64(rows[0].(map[string]any)["id"].(float64)))
	c := v1JSON(t, e.req("GET", "/api/v1/proposals?confirm=reject&id="+id, ck(adm), nil, nil))["confirm"].(map[string]any)
	if c["head"] == "" || len(c["ids"].([]any)) != 1 || c["danger"] != true || c["stop"] != nil {
		t.Fatal(c)
	}
	if s := v1JSON(t, e.req("GET", "/api/v1/proposals?confirm=reject", ck(adm), nil, nil))["confirm"].(map[string]any); s["stop"] == nil {
		t.Fatal(s)
	}
	back := "/review?kind=proposals&mode=full"
	if r := e.req("POST", "/api/v1/proposals/act", ck(adm), url.Values{"x": {"1"}}, nil); r.status != 403 {
		t.Fatal("no csrf", r.status)
	}
	if r := e.v1Post("/api/v1/proposals/act", ck(vw), map[string]any{"id": []any{id}, "action": "snooze"}, nil); r.status != 403 {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/proposals/act", ck(adm), map[string]any{"action": "snooze"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	r := e.v1Post("/api/v1/proposals/act", ck(adm), map[string]any{"id": []any{id}, "action": "reject", "reason": "Duplicate", "return": back}, nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.HasPrefix(m["redirect"].(string), back+"&req=") {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/proposals/settings", ck(adm), map[string]any{"expiry_days": "0", "reject_days": "5"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/proposals/settings", ck(adm), map[string]any{"expiry_days": "7", "reject_days": "5", "auto_qa": "1", "return": back}, nil); r.status != 200 {
		t.Fatal(r.status, r.body)
	}
}

func TestAPIv1ListsPolicyAndDemo(t *testing.T) {
	twins := map[string]string{"/api/v1/ideas/act": "/ideas/act", "/api/v1/ideas/generate": "/ideas/generate", "/api/v1/proposals/act": "/proposals/act", "/api/v1/proposals/settings": "/proposals/settings"}
	for api, page := range twins {
		if !apiIsPost(api) {
			t.Errorf("%s differs from %s", api, page)
		}
		if apiBodyLimit(api) != apiMaxBody && apiBodyLimit(api) != postLimits[page] {
			t.Errorf("%s body limit", api)
		}
	}
	e := iaSetup(t)
	nomfa, _ := e.loginNoMFA("admin")
	srvEnrol(t, nomfa)
	r := e.v1Post("/api/v1/proposals/settings", map[string]string{auth.COOKIE: nomfa}, map[string]any{"expiry_days": "7"}, nil)
	if m := v1JSON(t, r); r.status != 403 || m["mfa"] != "stepup" || m["sensitive"] != true {
		t.Fatal(r.status, r.body)
	}
	r = e.v1Post("/api/v1/proposals/act", map[string]string{auth.COOKIE: nomfa}, map[string]any{"id": []any{"1"}, "action": "snooze"}, nil)
	if m := v1JSON(t, r); r.status != 403 || m["mfa"] != "stepup" || m["sensitive"] != false {
		t.Fatal(r.status, r.body)
	}
	adm, _ := e.login("admin")
	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	for p := range twins {
		if r := e.v1Post(p, map[string]string{auth.COOKIE: adm}, map[string]any{"id": "1"}, nil); r.status != 403 || v1JSON(t, r)["error"] != "Disabled in the demo" {
			t.Fatal(p, r.status, r.body)
		}
	}
}

func TestAPIv1Lessons(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	if r := e.req("GET", "/api/v1/lessons", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := e.req("GET", "/api/v1/lessons", ck(vw), nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	d := v1JSON(t, e.req("GET", "/api/v1/lessons", ck(adm), nil, nil))
	rows := d["rows"].([]any)
	if len(rows) == 0 || len(d["tabs"].([]any)) != 4 || len(d["facets"].([]any)) != 3 || !strings.HasPrefix(d["here"].(string), "/review?kind=lessons&mode=full") {
		t.Fatal(v1Str(d)[:300])
	}
	if strings.Contains(v1Str(d), "<a ") || strings.Contains(v1Str(d), "/admin/lessons") {
		t.Fatal("html or an old link in the answer")
	}
	// the lessons' Kind filter travels as lkind: Review's kind names the list
	k := "investigate"
	f := v1JSON(t, e.req("GET", "/api/v1/lessons?kind=lessons&mode=full&lkind="+k, ck(adm), nil, nil))
	for _, x := range f["rows"].([]any) {
		if x.(map[string]any)["kind"] != k {
			t.Fatal(x)
		}
	}
	if !strings.Contains(f["list"].(map[string]any)["clearHref"].(string), "kind=lessons") || f["list"].(map[string]any)["filtered"] != true {
		t.Fatal(v1Str(f["list"]))
	}
	for _, p := range []string{"/api/v1/lessons/act", "/api/v1/lessons/settings"} {
		if !apiIsPost(p) || POST_ROUTES[p] != PolicySensitive {
			t.Fatal(p)
		}
		if r := e.req("POST", p, ck(adm), url.Values{"x": {"1"}}, nil); r.status != 403 {
			t.Fatal("no csrf", p, r.status)
		}
		if r := e.v1Post(p, ck(vw), map[string]any{"id": "1", "action": "approve"}, nil); r.status != 403 {
			t.Fatal("viewer", p, r.status, r.body)
		}
	}
	if apiBodyLimit("/api/v1/lessons/act") != postLimits["/admin/lessons/act"] {
		t.Fatal("body limit")
	}
	id := I2S(int64(rows[0].(map[string]any)["id"].(float64)))
	// a refused edit answers its sentence and writes nothing; a good action leaves one request file
	if r := e.v1Post("/api/v1/lessons/act", ck(adm), map[string]any{"id": id, "action": "edit", "title": "", "body": "x"}, nil); r.status != 400 || !strings.Contains(r.body, "Not saved: Title must be") {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/lessons/act", ck(adm), map[string]any{"id": "0", "action": "approve"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	dir := filepath.Join(SET_IN, "board-requests")
	before, _ := filepath.Glob(dir + "/*.json")
	r := e.v1Post("/api/v1/lessons/act", ck(adm), map[string]any{"id": id, "action": "approve", "return": "/review?kind=lessons&mode=full"}, nil)
	after, _ := filepath.Glob(dir + "/*.json")
	if r.status != 200 || len(after) != len(before)+1 || !strings.Contains(r.body, "mode=full") {
		t.Fatal(r.status, r.body, len(before), len(after))
	}
}

func TestAPIv1Suggestions(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	if r := e.req("GET", "/api/v1/suggestions", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if err := os.MkdirAll(filepath.Join(SET_IN, "problem-actions"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := v1JSON(t, e.req("GET", "/api/v1/suggestions", ck(adm), nil, nil))
	rows := d["rows"].([]any)
	if len(rows) == 0 || d["can"] != true || rows[0].(map[string]any)["canDecide"] != true || !strings.HasPrefix(d["here"].(string), "/review?kind=links&mode=full") {
		t.Fatal(v1Str(d)[:300])
	}
	if strings.Contains(v1Str(d), "<a ") || strings.Contains(v1Str(d), "view=suggestions") {
		t.Fatal("html or an old link in the answer")
	}
	v := v1JSON(t, e.req("GET", "/api/v1/suggestions", ck(vw), nil, nil))
	if v["can"] != false || v["rows"].([]any)[0].(map[string]any)["canDecide"] != false {
		t.Fatal(v1Str(v)[:200])
	}
	id := I2S(int64(rows[0].(map[string]any)["id"].(float64)))
	c := v1JSON(t, e.req("GET", "/api/v1/suggestions?confirm=reject&link="+id, ck(adm), nil, nil))["confirm"].(map[string]any)
	if c["head"] != "Reject 1 suggested link?" || len(c["links"].([]any)) != 1 {
		t.Fatal(v1Str(c))
	}
	if x := v1JSON(t, e.req("GET", "/api/v1/suggestions?confirm=reject&link="+id, ck(vw), nil, nil)); x["confirm"] != nil {
		t.Fatal("a viewer gets no confirm step")
	}
	if bad := v1JSON(t, e.req("GET", "/api/v1/suggestions?confirm=accept", ck(adm), nil, nil))["confirm"].(map[string]any); bad["error"] == nil {
		t.Fatal(v1Str(bad))
	}
}

func TestAPIv1Replies(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	if r := e.req("GET", "/api/v1/replies", ck(vw), nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	d := v1JSON(t, e.req("GET", "/api/v1/replies", ck(adm), nil, nil))
	rows := d["rows"].([]any)
	if len(rows) == 0 {
		t.Fatal(v1Str(d))
	}
	row := rows[0].(map[string]any)
	ch := row["choices"].([]any)
	if row["pid"] == "" || ch[len(ch)-1].(map[string]any)["label"] != "Unassigned" {
		t.Fatal(v1Str(row))
	}
}
