package outline

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sinaneysi/argus/internal/secret"
)

// testdata/merge-parity.json: the earlier installation's merge rule (its markers renamed to argus) on synthetic
// pages. The Go merge must give the same text, state and flags.
func TestMergeParity(t *testing.T) {
	b, err := os.ReadFile("testdata/merge-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []map[string]any
	json.Unmarshal(b, &cases)
	m := NewMarks("argus", "Argus")
	strs := func(v any) []string {
		out := []string{}
		for _, x := range v.([]any) {
			out = append(out, x.(string))
		}
		return out
	}
	for _, c := range cases {
		if c["name"] == "chain" {
			continue
		}
		text, state, flags := m.Merge(c["remote"].(string), c["block"].(string), strs(c["known"]))
		if text != c["text"] || state != c["state"] || !reflect.DeepEqual(flags, strs(c["flags"])) {
			t.Fatalf("%s: %q %s %v\nwant %q %s %v", c["name"], text, state, flags, c["text"], c["state"], c["flags"])
		}
		if _, s2, _ := m.Merge(text, c["block"].(string), strs(c["known"])); s2 != c["again_state"] {
			t.Fatalf("%s: second merge %s", c["name"], s2)
		}
		if BlockHash(c["block"].(string)) != c["hash"] {
			t.Fatalf("%s: hash", c["name"])
		}
		p, ok := m.Proposed(text)
		if want, has := c["proposed"].(string); has != ok || p != want {
			t.Fatalf("%s: proposed %q %v", c["name"], p, ok)
		}
	}
	ch := cases[len(cases)-1]
	k := []string{BlockHash("Argus text")}
	t1, _, _ := m.Merge(cases[3]["remote"].(string), "Argus v2", k)
	t3, s3, _ := m.Merge(t1, "Argus v3", k)
	_, blk, _, _ := m.Split(t3)
	acc := strings.Replace(t3, blk, "Argus v3", 1)
	t4, s4, f4 := m.Merge(acc, "Argus v3", nil)
	if t3 != ch["t3"] || s3 != ch["s3"] || t4 != ch["t4"] || s4 != ch["s4"] || len(f4) != 0 {
		t.Fatalf("chain: %q %s / %q %s", t3, s3, t4, s4)
	}
}

func TestGuard(t *testing.T) {
	hosts := []string{"docs.example.test"}
	for url, want := range map[string]string{
		"http://docs.example.test":           "https://",
		"https://u:p@docs.example.test":      "user name",
		"https://docs.example.test/?x=1":     "query",
		"https://evil.example":               "not an allowed",
		"https://docs.example.test:8443":     "not an allowed",
		"https://docs.example.test/a b":      "not valid",
		"https://docs.example.test/a%2e%2e/": "path",
	} {
		if _, _, _, err := CheckURL(url, hosts, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (want %q)", url, err, want)
		}
	}
	for ip, want := range map[string]string{"127.0.0.1": "loopback", "169.254.169.254": "a cloud metadata", "169.254.1.1": "link-local",
		"0.0.0.0": "unspecified", "224.0.0.1": "multicast", "240.0.0.1": "reserved", "::1": "loopback", "10.0.0.5": ""} {
		if got := BadAddress(net.ParseIP(ip), false); got != want {
			t.Errorf("%s: %q want %q", ip, got, want)
		}
	}
	if _, _, _, err := CheckURL("https://169.254.169.254", []string{"169.254.169.254"}, false); err == nil {
		t.Fatal("metadata address allowed")
	}
}

const goodToken = "ol_api_good-token-1234567890"

// fake is a local Outline: the eight methods, Bearer tokens, parentDocumentId honoured.
type fake struct {
	mu               sync.Mutex
	cols             []map[string]any
	docs             map[string]map[string]any
	calls            []string
	bodies           []map[string]any
	basic, roundtrip bool
	rate             int
	n                int
	srv              *httptest.Server
}

func newFake(t *testing.T) *fake {
	f := &fake{cols: []map[string]any{{"id": "col-eng", "name": "Engineering"}}, docs: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) out(d map[string]any) map[string]any {
	c := map[string]any{}
	for k, v := range d {
		c[k] = v
	}
	if f.roundtrip {
		s := regexp.MustCompile(`<!--(.*?)-->`).ReplaceAllString(c["text"].(string), `\<!--$1--\>`)
		c["text"] = regexp.MustCompile(`(?m)^- `).ReplaceAllString(s, "* ")
	}
	return c
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	f.calls = append(f.calls, method)
	f.bodies = append(f.bodies, body)
	send := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
	if f.basic {
		w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
		send(401, map[string]any{})
		return
	}
	if f.rate > 0 {
		f.rate--
		w.Header().Set("Retry-After", "0")
		send(429, map[string]any{"error": "rate_limit_exceeded"})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+goodToken {
		send(401, map[string]any{"ok": false, "error": "authentication_required"})
		return
	}
	ok := func(v any) { send(200, map[string]any{"ok": true, "data": v}) }
	switch method {
	case "auth.info":
		ok(map[string]any{"user": map[string]any{"name": "Dana"}, "team": map[string]any{"name": "Example Team"}})
	case "collections.list":
		ok(f.cols)
	case "collections.create":
		c := map[string]any{"id": fmt.Sprintf("col-%d", len(f.cols)), "name": body["name"]}
		f.cols = append(f.cols, c)
		ok(c)
	case "documents.create":
		parent, _ := body["parentDocumentId"].(string)
		if p, has := f.docs[parent]; parent != "" && (!has || p["collectionId"] != body["collectionId"]) {
			send(400, map[string]any{"error": "validation_error", "message": "parentDocumentId invalid"})
			return
		}
		f.n++
		id := fmt.Sprintf("doc%03d", f.n)
		d := map[string]any{"id": id, "title": body["title"], "text": body["text"], "collectionId": body["collectionId"],
			"parentDocumentId": nil, "url": "/doc/" + id}
		if parent != "" {
			d["parentDocumentId"] = parent
		}
		f.docs[id] = d
		ok(f.out(d))
	case "documents.update":
		d := f.docs[body["id"].(string)]
		if d == nil {
			send(404, map[string]any{"error": "not_found"})
			return
		}
		d["text"] = body["text"]
		ok(f.out(d))
	case "documents.info":
		d := f.docs[fmt.Sprint(body["id"])]
		if d == nil {
			send(404, map[string]any{"error": "not_found"})
			return
		}
		ok(f.out(d))
	case "documents.list":
		var out []map[string]any
		for _, d := range f.docs {
			if d["collectionId"] == body["collectionId"] && d["deletedAt"] == nil {
				out = append(out, f.out(d))
			}
		}
		if off, _ := body["offset"].(float64); off > 0 {
			out = nil
		}
		ok(out)
	default:
		send(404, map[string]any{"error": "not_found"})
	}
}

func (f *fake) methodsFrom(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls[i:]...)
}

type env struct {
	t  *testing.T
	o  *Options
	f  *fake
	db *sql.DB
	d  string
}

func newEnv(t *testing.T) *env {
	d := t.TempDir()
	f := newFake(t)
	for _, s := range []string{"in", "out", "etc", "state"} {
		os.MkdirAll(filepath.Join(d, s), 0o755)
	}
	auth, _ := sql.Open("sqlite", "file:"+filepath.Join(d, "auth.db"))
	auth.Exec("CREATE TABLE users (username TEXT PRIMARY KEY, role TEXT, disabled INTEGER)")
	auth.Exec("INSERT INTO users VALUES ('admin.a','admin',0),('viewer.v','viewer',0),('gone.g','admin',1)")
	auth.Close()
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(d, "ops.db"))
	db.SetMaxOpenConns(1)
	db.Exec(`CREATE TABLE outline_pages (service TEXT PRIMARY KEY, grp TEXT, title TEXT NOT NULL, body TEXT NOT NULL, body_hash TEXT NOT NULL,
 prev_body TEXT, prev_hash TEXT, rendered_at INTEGER, changed_at INTEGER, redacted TEXT, blocked TEXT, summary TEXT,
 doc_id TEXT, doc_url TEXT, publish_status TEXT, publish_flags TEXT, published_at INTEGER, publish_error TEXT)`)
	for _, p := range [][3]string{{"shop/payment", "shop/payment", "## Summary\n\nPays."}, {"shop/sms", "shop/sms", "## Summary\n\nTexts."},
		{"shop/other", "shop/other", "## Summary\n\nNot in the pilot."}, {Index, "Services overview", "- shop/payment\n- shop/sms"}} {
		db.Exec("INSERT INTO outline_pages (service, title, body, body_hash) VALUES (?,?,?,?)", p[0], p[1], p[2], BlockHash(p[2]))
	}
	t.Cleanup(func() { db.Close() })
	host := strings.TrimPrefix(f.srv.URL, "http://")
	os.WriteFile(filepath.Join(d, "etc", "outline-allowed-hosts"), []byte(host+"\n"), 0o644)
	key, _ := secret.ParseKey(secret.NewKeyText())
	o := &Options{ReqDir: filepath.Join(d, "in"), OutDir: filepath.Join(d, "out"), StateFile: filepath.Join(d, "state", "state.json"),
		EtcDir: filepath.Join(d, "etc"), AuthDB: filepath.Join(d, "auth.db"), DB: db, Key: key, DefaultURL: f.srv.URL,
		Pilot: []string{"shop/payment", "shop/sms"}, AllowLocal: true, Sleep: func(time.Duration) {}, AppUID: -1}
	return &env{t: t, o: o, f: f, db: db, d: d}
}

func (e *env) request(user, rid string, req map[string]any) string {
	e.t.Helper()
	req["user"] = user
	b, _ := json.Marshal(req)
	if err := os.WriteFile(filepath.Join(e.o.ReqDir, user+"-"+rid+".json"), b, 0o600); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.o.Requests(); err != nil {
		e.t.Fatal(err)
	}
	return e.status()["results"].(map[string]any)[rid].(map[string]any)["result"].(string)
}

func (e *env) status() map[string]any {
	var st map[string]any
	b, _ := os.ReadFile(filepath.Join(e.o.OutDir, "outline-status.json"))
	json.Unmarshal(b, &st)
	return st
}

func (e *env) connect(rid string, kw map[string]any) string {
	req := map[string]any{"kind": "connect", "url": e.f.srv.URL, "token": goodToken, "collection": "Services", "create_collection": true}
	for k, v := range kw {
		req[k] = v
	}
	return e.request("admin.a", rid, req)
}

func (e *env) meta() Meta { return e.o.meta() }

func (e *env) publish() Run {
	e.t.Helper()
	r, err := e.o.Publish()
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func TestConnectStoresTheTokenSealed(t *testing.T) {
	e := newEnv(t)
	res := e.connect("0123456789abcdef", nil)
	if res != "connected as Dana (Example Team); collection Services created; pages go under Argus (created)" {
		t.Fatal(res)
	}
	b, _ := os.ReadFile(filepath.Join(e.o.EtcDir, "outline.token"))
	fi, _ := os.Stat(filepath.Join(e.o.EtcDir, "outline.token"))
	if !secret.Sealed(string(b)) || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token not sealed 0600: %v", fi.Mode())
	}
	filepath.Walk(e.d, func(p string, info os.FileInfo, err error) error {
		if info != nil && !info.IsDir() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), goodToken) {
				t.Errorf("the token is in %s", p)
			}
		}
		return nil
	})
	if names, _ := os.ReadDir(e.o.ReqDir); len(names) != 0 {
		t.Fatal("request not consumed")
	}
	if r := e.connect("1123456789abcdef", map[string]any{"create_collection": false}); r != "connected as Dana (Example Team); collection Services; pages go under Argus" {
		t.Fatal(r)
	}
	if len(e.f.docs) != 1 || e.status()["connection"].(map[string]any)["parent"] != "Argus" {
		t.Fatalf("parent created twice or not in the status: %v", e.f.docs)
	}
}

func TestRefusals(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		user, rid string
		req       map[string]any
		want      string
	}{
		{"viewer.v", "0000000000000001", map[string]any{"kind": "sync"}, "only admins"},
		{"gone.g", "0000000000000002", map[string]any{"kind": "sync"}, "unknown or disabled"},
		{"admin.a", "0000000000000003", map[string]any{"kind": "connect", "token": "short"}, "API key"},
		{"admin.a", "0000000000000004", map[string]any{"kind": "connect", "token": goodToken, "url": "https://evil.example"}, "not an allowed Outline host"},
		{"admin.a", "0000000000000005", map[string]any{"kind": "connect", "token": "ol_api_wrong-token-1234567890", "url": e.f.srv.URL}, "HTTP 401"},
		{"admin.a", "0000000000000006", map[string]any{"kind": "connect", "token": goodToken, "url": e.f.srv.URL, "collection": "Nope"}, "no collection called"},
		{"admin.a", "0000000000000007", map[string]any{"kind": "settings", "mode": "publish"}, "connect Outline"},
	} {
		if r := e.request(c.user, c.rid, c.req); !strings.Contains(r, c.want) || strings.Contains(r, "wrong-token") {
			t.Errorf("%s: %q (want %q)", c.rid, r, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(e.o.EtcDir, "outline.token")); err == nil {
		t.Fatal("a refused connect stored a token")
	}
	b, _ := json.Marshal(map[string]any{"kind": "sync", "user": "viewer.v"})
	os.WriteFile(filepath.Join(e.o.ReqDir, "admin.a-0000000000000008.json"), b, 0o600)
	e.o.Requests()
	if r := e.status()["results"].(map[string]any)["0000000000000008"].(map[string]any)["result"].(string); !strings.Contains(r, "does not match") {
		t.Fatal(r)
	}
}

func TestPreviewSendsNothing(t *testing.T) {
	e := newEnv(t)
	e.connect("0123456789abcdef", nil)
	n := len(e.f.calls)
	if r := e.publish(); r.Mode != "preview" || len(e.f.calls) != n {
		t.Fatalf("%+v %d", r, len(e.f.calls)-n)
	}
}

func TestPublishIdempotentKeepsHumanText(t *testing.T) {
	e := newEnv(t)
	e.connect("0123456789abcdef", nil)
	e.request("admin.a", "1123456789abcdef", map[string]any{"kind": "settings", "mode": "publish", "scope": "pilot"})
	e.f.roundtrip = true
	r := e.publish()
	if !reflect.DeepEqual(r.Counts, map[string]int{"created": 3}) || r.Parent != "Argus" {
		t.Fatalf("%+v", r)
	}
	var docID, docURL, ps string
	e.db.QueryRow("SELECT doc_id, doc_url, publish_status FROM outline_pages WHERE service='shop/payment'").Scan(&docID, &docURL, &ps)
	if docID == "" || !strings.HasPrefix(docURL, e.f.srv.URL+"/doc/") || ps != "created" {
		t.Fatalf("copy back: %q %q %q", docID, docURL, ps)
	}
	n := len(e.f.calls)
	if r := e.publish(); !reflect.DeepEqual(r.Counts, map[string]int{"unchanged": 3}) {
		t.Fatalf("%+v", r)
	}
	for _, m := range e.f.methodsFrom(n) {
		if m == "documents.update" || m == "documents.create" {
			t.Fatal("a run with nothing new wrote")
		}
	}
	var smsID string
	e.db.QueryRow("SELECT doc_id FROM outline_pages WHERE service='shop/sms'").Scan(&smsID)
	e.f.mu.Lock()
	e.f.docs[docID]["text"] = strings.Replace(e.f.docs[docID]["text"].(string), "Write your own notes here.", "Owner: payments team.", 1)
	e.f.docs[smsID]["text"] = strings.Replace(e.f.docs[smsID]["text"].(string), "## Summary", "## Summary\n\nA person's correction.", 1)
	e.f.mu.Unlock()
	e.db.Exec("UPDATE outline_pages SET body='## Summary\n\nPays.\n- New trap.' WHERE service='shop/payment'")
	e.db.Exec("UPDATE outline_pages SET body='## Summary\n\nTexts v2.' WHERE service='shop/sms'")
	e.publish()
	st := e.status()["pages"].(map[string]any)
	if st["shop/payment"].(map[string]any)["state"] != "updated" || st["shop/sms"].(map[string]any)["state"] != "human-edit" {
		t.Fatalf("%v", st)
	}
	pt, stx := e.f.docs[docID]["text"].(string), e.f.docs[smsID]["text"].(string)
	m := e.o.Marks
	_, pblock, _, _ := m.Split(pt)
	_, sblock, _, _ := m.Split(stx)
	prop, _ := m.Proposed(stx)
	if !strings.Contains(pt, "Owner: payments team.") || !strings.Contains(pblock, "New trap.") ||
		!strings.Contains(sblock, "A person's correction.") || !strings.Contains(prop, "Texts v2.") {
		t.Fatalf("merge lost text:\n%s\n---\n%s", pt, stx)
	}
	// lost state: the pages are found again by title among the parent's children, never duplicated
	os.Remove(e.o.StateFile)
	e.db.Exec("UPDATE outline_pages SET doc_id=NULL")
	e.publish()
	if len(e.f.docs) != 4 {
		t.Fatalf("duplicated: %d docs", len(e.f.docs))
	}
}

func TestPagesUnderParentNeverAtRoot(t *testing.T) {
	e := newEnv(t)
	e.connect("0123456789abcdef", nil)
	e.request("admin.a", "1123456789abcdef", map[string]any{"kind": "settings", "mode": "publish"})
	pid, cid := e.meta().s("parent_id"), e.meta().s("collection_id")
	e.f.docs["mine"] = map[string]any{"id": "mine", "title": "shop/payment", "text": "my own notes", "collectionId": cid, "parentDocumentId": nil}
	r := e.publish()
	if !reflect.DeepEqual(r.Counts, map[string]int{"created": 3}) {
		t.Fatalf("%+v", r)
	}
	for id, d := range e.f.docs {
		if id != pid && id != "mine" && d["parentDocumentId"] != pid {
			t.Fatalf("a page outside the parent: %v", d)
		}
	}
	if e.f.docs["mine"]["text"] != "my own notes" {
		t.Fatal("a person's page was adopted by title")
	}
	var sid string
	e.db.QueryRow("SELECT doc_id FROM outline_pages WHERE service='shop/payment'").Scan(&sid)
	e.f.docs[sid]["deletedAt"] = "2026-11-01T00:00:00Z"
	n := len(e.f.calls)
	e.publish()
	p := e.status()["pages"].(map[string]any)["shop/payment"].(map[string]any)
	if fmt.Sprint(p["flags"]) != "[recreated]" || p["doc_id"] == sid {
		t.Fatalf("%v", p)
	}
	for i, m := range e.f.methodsFrom(n) {
		if m == "documents.update" && e.f.bodies[n+i]["id"] == sid {
			t.Fatal("a trashed page was written")
		}
	}
}

func TestMissingParentRefuses(t *testing.T) {
	e := newEnv(t)
	e.connect("0123456789abcdef", nil)
	e.request("admin.a", "1123456789abcdef", map[string]any{"kind": "settings", "mode": "publish"})
	pid := e.meta().s("parent_id")
	for _, c := range []struct {
		brk func()
		why string
	}{{func() { e.f.docs[pid]["deletedAt"] = "x" }, "it is in the trash"}, {func() { delete(e.f.docs, pid) }, "not found"}} {
		c.brk()
		n, docs := len(e.f.calls), len(e.f.docs)
		r := e.publish()
		if !strings.Contains(r.Error, "cannot be used: "+c.why) || !strings.Contains(r.Error, "never falls back to the collection root") || len(r.Counts) != 0 {
			t.Fatalf("%+v", r)
		}
		if m := e.f.methodsFrom(n); !reflect.DeepEqual(m, []string{"documents.info"}) || len(e.f.docs) != docs {
			t.Fatalf("calls after a bad parent: %v", m)
		}
	}
	m := e.meta()
	delete(m, "parent_id")
	e.o.saveMeta(m)
	if r := e.publish(); !strings.Contains(r.Error, "no parent document is configured") {
		t.Fatal(r.Error)
	}
	if r := e.request("admin.a", "2123456789abcdef", map[string]any{"kind": "settings", "mode": "publish"}); !strings.Contains(r, "parent document") {
		t.Fatal(r)
	}
}

func TestConnectNeedsTopLevelParent(t *testing.T) {
	e := newEnv(t)
	e.connect("0123456789abcdef", nil)
	cid := e.meta().s("collection_id")
	if r := e.connect("1123456789abcdef", map[string]any{"create_collection": false, "parent": "Handbook"}); !strings.Contains(r, `no top-level document called "Handbook"`) {
		t.Fatal(r)
	}
	e.f.docs["top"] = map[string]any{"id": "top", "title": "Handbook", "text": "", "collectionId": cid, "parentDocumentId": nil}
	e.f.docs["nested"] = map[string]any{"id": "nested", "title": "Handbook", "text": "", "collectionId": cid, "parentDocumentId": "top"}
	if r := e.connect("2123456789abcdef", map[string]any{"create_collection": false, "parent": "handbook"}); !strings.HasSuffix(r, "pages go under Handbook") {
		t.Fatal(r)
	}
	if e.meta().s("parent_id") != "top" {
		t.Fatal(e.meta())
	}
	e.connect("3123456789abcdef", map[string]any{"create_collection": false})
	if e.meta().s("parent_id") != "top" {
		t.Fatal("a reconnect without a parent lost the stored one")
	}
	e.f.docs["t1"] = map[string]any{"id": "t1", "title": "Twin", "text": "", "collectionId": cid, "parentDocumentId": nil}
	e.f.docs["t2"] = map[string]any{"id": "t2", "title": "Twin", "text": "", "collectionId": cid, "parentDocumentId": nil}
	if r := e.connect("4123456789abcdef", map[string]any{"parent": "Twin"}); !strings.Contains(r, `2 top-level documents are called "Twin"`) {
		t.Fatal(r)
	}
	if e.meta().s("parent_id") != "top" {
		t.Fatal("a refused connect changed the parent")
	}
}

func TestBlockedBasicAuthDisconnect(t *testing.T) {
	e := newEnv(t)
	e.connect("0123456789abcdef", nil)
	e.request("admin.a", "1123456789abcdef", map[string]any{"kind": "settings", "mode": "publish"})
	e.db.Exec("UPDATE outline_pages SET blocked='secret scan: still matches' WHERE service='shop/payment'")
	e.publish()
	if e.status()["pages"].(map[string]any)["shop/payment"].(map[string]any)["state"] != "blocked" {
		t.Fatal("blocked page published")
	}
	for _, d := range e.f.docs {
		if d["title"] == "shop/payment" {
			t.Fatal("blocked page sent")
		}
	}
	if r := e.request("admin.a", "2123456789abcdef", map[string]any{"kind": "disconnect"}); !strings.Contains(r, "disconnected") || e.status()["connected"] != false {
		t.Fatal(r)
	}
	e.f.basic = true
	if r := e.connect("3123456789abcdef", nil); !strings.Contains(r, "basic-auth challenge") {
		t.Fatal(r)
	}
}

func TestClientRules(t *testing.T) {
	f := newFake(t)
	c, _ := New(f.srv.URL, goodToken, "", true)
	c.Sleep = func(time.Duration) {}
	if _, err := c.Call("documents.delete", nil); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatal("a delete was allowed")
	}
	if _, err := New("http://docs.example.test", goodToken, "", false); err == nil {
		t.Fatal("http allowed")
	}
	f.rate = 2
	if _, err := c.AuthInfo(); err != nil {
		t.Fatal(err)
	}
	c.Budget = c.Calls
	if _, err := c.AuthInfo(); err == nil || !err.(*Error).Budget {
		t.Fatal("budget not enforced")
	}
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	}))
	defer redir.Close()
	c2, _ := New(redir.URL, goodToken, "", true)
	if _, err := c2.AuthInfo(); err == nil || !strings.Contains(err.Error(), "redirect refused") {
		t.Fatal(err)
	}
}
