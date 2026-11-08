package outline

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/secret"
)

// TokenName is the name the Outline API key is sealed under (ARGUS_ETC_DIR/outline.token).
const TokenName = "etc/outline.token"

// Index is the service key of the "Services overview" page.
const Index = "_index"

// Options configure the requests and the publish.
type Options struct {
	ReqDir     string // settings-in/outline-requests
	OutDir     string // settings-out: outline-status.json, audit.log
	StateFile  string // per-page state (doc ids, known block hashes)
	EtcDir     string // outline.token (0600, sealed), outline.json (the connection), outline-allowed-hosts
	AuthDB     string // users and roles
	DB         *sql.DB
	Key        secret.Key // seals the token when set
	DefaultURL string
	// DefaultHosts are allowed when the hosts file does not exist (the host of DefaultURL).
	DefaultHosts  []string
	DefaultParent string   // the parent document's title (OUTLINE_PARENT)
	IndexTitle    string   // "Services overview"
	Pilot         []string // the services of scope pilot (OUTLINE_PILOT)
	Marks         *Marks
	Name          string // product name in texts
	// SourceURL and SourceToken: the Outline entry of Settings -> Data sources (purpose docs), used when no token
	// was stored by a connect.
	SourceURL, SourceToken string
	Budget                 int
	AllowLocal             bool // tests only: http://127.0.0.1
	AppUID                 int
	Sleep                  func(time.Duration)
	Now                    func() time.Time
	Log                    io.Writer
}

func (o *Options) defaults() {
	if o.DefaultParent == "" {
		o.DefaultParent = "Argus"
	}
	if o.IndexTitle == "" {
		o.IndexTitle = "Services overview"
	}
	if o.Name == "" {
		o.Name = "Argus"
	}
	if o.Marks == nil {
		o.Marks = NewMarks("", o.Name)
	}
	if o.Budget <= 0 {
		o.Budget = 600
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
}

func (o *Options) now() int64 { return o.Now().Unix() }

var (
	nameRe  = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._@-]{0,63})-([0-9a-f]{16})\.json$`)
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{20,200}$`)
	collRe  = regexp.MustCompile(`^[^\x00-\x1f<>]{1,100}$`)
)

const maxRequest = 16 * 1024

// Meta is the stored connection (no secret).
type Meta map[string]any

func (m Meta) s(k string) string { v, _ := m[k].(string); return v }

func (o *Options) path(n string) string { return filepath.Join(o.EtcDir, n) }

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	f.Close()
	os.Chmod(tmp, mode)
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, v) == nil
}

func (o *Options) audit(line string) {
	f, err := os.OpenFile(filepath.Join(o.OutDir, "audit.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintf(f, "%s %s\n", o.Now().Format("2006-01-02 15:04:05"), line)
		f.Close()
	}
}

// AllowedHosts is the hosts file, else the default hosts.
func (o *Options) AllowedHosts() []string {
	b, err := os.ReadFile(o.path("outline-allowed-hosts"))
	if err != nil {
		return append([]string{}, o.DefaultHosts...)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" && !strings.HasPrefix(l, "#") && HostOK(l) {
			out = append(out, l)
		}
	}
	return out
}

func (o *Options) storedToken() string {
	p := o.path("outline.token")
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return ""
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if secret.Sealed(v) {
		plain, err := o.Key.Open(TokenName, v)
		if err != nil {
			return ""
		}
		return plain
	}
	return v
}

// token is the stored token, else the Data sources entry's when its address is the connection's.
func (o *Options) token(m Meta) string {
	if t := o.storedToken(); t != "" {
		return t
	}
	if o.SourceToken != "" && m.s("url") != "" && strings.TrimRight(o.SourceURL, "/") == m.s("url") {
		return o.SourceToken
	}
	return ""
}

func (o *Options) saveToken(t string) error {
	v := t
	if o.Key != nil {
		s, err := o.Key.Seal(TokenName, t)
		if err != nil {
			return err
		}
		v = s
	}
	return writeFile(o.path("outline.token"), []byte(v), 0o600)
}

func (o *Options) meta() Meta {
	m := Meta{}
	readJSON(o.path("outline.json"), &m)
	return m
}

func (o *Options) saveMeta(m Meta) error {
	b, _ := json.MarshalIndent(m, "", " ")
	return writeFile(o.path("outline.json"), b, 0o600)
}

func (o *Options) client(base, token, pin string) (*Client, error) {
	c, err := New(base, token, pin, o.AllowLocal)
	if err != nil {
		return nil, err
	}
	c.Sleep = o.Sleep
	return c, nil
}

func (o *Options) storedClient(m Meta) (*Client, error) {
	tok := o.token(m)
	if tok == "" || m.s("url") == "" {
		return nil, Refused("Outline is not connected")
	}
	base, _, ip, err := CheckURL(m.s("url"), o.AllowedHosts(), o.AllowLocal)
	if err != nil {
		return nil, err
	}
	c, err := o.client(base, tok, ip)
	if c != nil {
		c.Budget = o.Budget
	}
	return c, err
}

// --- requests ----------------------------------------------------------------------------------------------

func (o *Options) userRole(name string) string {
	db, err := sql.Open("sqlite", "file:"+o.AuthDB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return ""
	}
	defer db.Close()
	var role string
	var disabled int
	if db.QueryRow("SELECT role, disabled FROM users WHERE username=?", name).Scan(&role, &disabled) != nil || disabled != 0 {
		return ""
	}
	if role != "admin" && role != "viewer" {
		return ""
	}
	return role
}

func (o *Options) readRequest(path string) (map[string]any, error) {
	defer os.Remove(path)
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, Refused("cannot open request")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, Refused("request is not a regular file")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != o.AppUID && int(sys.Uid) != os.Geteuid() {
		return nil, Refused("request not written by the app")
	}
	data, _ := io.ReadAll(io.LimitReader(f, maxRequest+1))
	f.WriteAt(make([]byte, len(data)), 0) // the token does not stay on disk
	f.Sync()
	if len(data) > maxRequest {
		return nil, Refused("request file too large")
	}
	var req map[string]any
	if json.Unmarshal(data, &req) != nil || req == nil {
		return nil, Refused("request is not a JSON object")
	}
	return req, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func (o *Options) pickCollection(c *Client, name string, create bool) (string, string, bool, error) {
	cols, err := c.Collections()
	if err != nil {
		return "", "", false, err
	}
	for _, col := range cols {
		if lower(str(col["name"])) == lower(name) && str(col["id"]) != "" {
			return str(col["id"]), str(col["name"]), false, nil
		}
	}
	if !create {
		var have []string
		for _, col := range cols {
			n := str(col["name"])
			if len(n) > 40 {
				n = n[:40]
			}
			have = append(have, n)
		}
		sort.Strings(have)
		if len(have) > 15 {
			have = have[:15]
		}
		list := strings.Join(have, ", ")
		if list == "" {
			list = "none"
		}
		return "", "", false, Refused(fmt.Sprintf("no collection called %q is visible to this token (visible: %s); tick \"Create it\" to let %s create it", name, list, o.Name))
	}
	col, err := c.CreateCollection(name, "Service documentation generated and kept current by "+o.Name+": one page per service "+
		"and a Services overview. Write your own notes under Team notes on each page.")
	if err != nil {
		return "", "", false, err
	}
	if str(col["id"]) == "" {
		return "", "", false, Refused("Outline did not return the new collection")
	}
	return str(col["id"]), orS(str(col["name"]), name), true, nil
}

// usableParent is why a document cannot hold the pages ("" when it can).
func usableParent(d map[string]any, collection string) string {
	switch {
	case str(d["id"]) == "":
		return "not found"
	case str(d["collectionId"]) != "" && str(d["collectionId"]) != collection:
		return "it is in another collection"
	case d["deletedAt"] != nil && str(d["deletedAt"]) != "":
		return "it is in the trash"
	case d["archivedAt"] != nil && str(d["archivedAt"]) != "":
		return "it is archived"
	}
	return ""
}

func notFoundish(err error) bool {
	e, ok := asErr(err)
	return ok && (e.Status == 400 || e.Status == 403 || e.Status == 404)
}

// pickParent is the parent page: the stored id if still usable and still so named, else the top-level document of
// the collection with that title, created on request (only the parent itself is ever made at the root).
func (o *Options) pickParent(c *Client, collection, name, oldID string, create bool) (string, string, bool, error) {
	if oldID != "" {
		d, err := c.Info(oldID)
		if err != nil && !notFoundish(err) {
			return "", "", false, err
		}
		if err == nil && usableParent(d, collection) == "" && lower(str(d["title"])) == lower(name) {
			return str(d["id"]), orS(str(d["title"]), name), false, nil
		}
	}
	docs, err := c.Documents(collection)
	if err != nil {
		return "", "", false, err
	}
	var found []map[string]any
	for _, d := range docs {
		if str(d["id"]) != "" && str(d["parentDocumentId"]) == "" && lower(str(d["title"])) == lower(name) {
			found = append(found, d)
		}
	}
	if len(found) > 1 {
		return "", "", false, Refused(fmt.Sprintf("%d top-level documents are called %q in this collection; rename all but one", len(found), name))
	}
	if len(found) == 1 {
		return str(found[0]["id"]), orS(str(found[0]["title"]), name), false, nil
	}
	if !create {
		return "", "", false, Refused(fmt.Sprintf("no top-level document called %q in this collection; create it in Outline (an empty page "+
			"is enough) or tick \"Create it\" to let %s create it", name, o.Name))
	}
	d, err := c.Create(name, "Service documentation generated and kept current by "+o.Name+": one page per service and a "+
		"Services overview, all under this page.", collection, "")
	if err != nil {
		return "", "", false, err
	}
	if str(d["id"]) == "" {
		return "", "", false, Refused("Outline did not return the new parent document")
	}
	return str(d["id"]), orS(str(d["title"]), name), true, nil
}

func orS(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func refuseErr(err error) error {
	if e, ok := asErr(err); ok {
		if e.Auth {
			return Refused(e.Msg + ". Check that the whole API key was pasted and that it has not expired.")
		}
		return Refused(e.Msg)
	}
	return err
}

func (o *Options) handle(req map[string]any, user, role string) (string, error) {
	kind := str(req["kind"])
	if role != "admin" {
		return "", Refused("only admins can change the Outline integration")
	}
	m := o.meta()
	switch kind {
	case "connect":
		token := str(req["token"])
		if !tokenRe.MatchString(token) {
			return "", Refused("that does not look like an Outline API key (Outline: Settings → API → New API key)")
		}
		coll := strings.TrimSpace(orS(str(req["collection"]), "Services"))
		if !collRe.MatchString(coll) {
			return "", Refused("the collection name must be 1-100 characters, no < >")
		}
		same := lower(m.s("collection")) == lower(coll)
		stored := ""
		if same {
			stored = m.s("parent")
		}
		parent := strings.TrimSpace(orS(orS(str(req["parent"]), stored), o.DefaultParent))
		if !collRe.MatchString(parent) {
			return "", Refused("the parent document title must be 1-100 characters, no < >")
		}
		mode := orS(orS(str(req["mode"]), m.s("mode")), "preview")
		if mode != "preview" && mode != "publish" {
			return "", Refused("choose preview or publish")
		}
		base, key, ip, err := CheckURL(orS(str(req["url"]), o.DefaultURL), o.AllowedHosts(), o.AllowLocal)
		if err != nil {
			return "", err
		}
		c, err := o.client(base, token, ip)
		if err != nil {
			return "", refuseErr(err)
		}
		c.Retries, c.Budget, c.MinGap = 1, 20, 0
		create, _ := req["create_collection"].(bool)
		info, err := c.AuthInfo()
		if err != nil {
			return "", refuseErr(err)
		}
		cid, cname, created, err := o.pickCollection(c, coll, create)
		if err != nil {
			return "", refuseErr(err)
		}
		oldParent := ""
		if m.s("collection_id") == cid {
			oldParent = m.s("parent_id")
		}
		pid, pname, pcreated, err := o.pickParent(c, cid, parent, oldParent, create)
		if err != nil {
			return "", refuseErr(err)
		}
		u, _ := info["user"].(map[string]any)
		t, _ := info["team"].(map[string]any)
		if err := o.saveToken(token); err != nil {
			return "", err
		}
		nm := Meta{"url": base, "host": key, "mode": mode, "scope": orS(m.s("scope"), "pilot"), "collection": cname,
			"collection_id": cid, "parent": pname, "parent_id": pid, "user": head(str(u["name"]), 80),
			"user_email": head(str(u["email"]), 120), "team": head(str(t["name"]), 80), "connected_at": o.now(), "by": user}
		if err := o.saveMeta(nm); err != nil {
			return "", err
		}
		o.audit(fmt.Sprintf("outline: connected to %s as %s (team %s), collection %s%s, parent %s%s, mode %s, by %s", key, nm.s("user"),
			nm.s("team"), cname, ifS(created, " (created)"), pname, ifS(pcreated, " (created)"), mode, user))
		return fmt.Sprintf("connected as %s (%s); collection %s%s; pages go under %s%s", orS(nm.s("user"), "?"), orS(nm.s("team"), "team ?"),
			cname, ifS(created, " created"), pname, ifS(pcreated, " (created)")), nil
	case "settings":
		mode, scope := orS(orS(str(req["mode"]), m.s("mode")), "preview"), orS(orS(str(req["scope"]), m.s("scope")), "pilot")
		if (mode != "preview" && mode != "publish") || (scope != "pilot" && scope != "all") {
			return "", Refused("choose preview or publish, and the pilot or all services")
		}
		if mode == "publish" && (m.s("url") == "" || o.token(m) == "" || m.s("collection_id") == "" || m.s("parent_id") == "") {
			return "", Refused("connect Outline (URL, API key, collection, parent document) before switching to publish")
		}
		m["mode"], m["scope"] = mode, scope
		if err := o.saveMeta(m); err != nil {
			return "", err
		}
		o.audit(fmt.Sprintf("outline: settings by %s: mode=%s scope=%s", user, mode, scope))
		what := "the pilot services"
		if scope == "all" {
			what = "all services"
		}
		return fmt.Sprintf("saved: %s mode, %s", mode, what), nil
	case "sync":
		o.audit("outline: sync requested by " + user)
		if m.s("mode") == "publish" {
			return "sync started", nil
		}
		return "previews refreshed (preview mode: nothing is sent to Outline)", nil
	case "disconnect":
		os.Remove(o.path("outline.token"))
		keep := Meta{"mode": "preview"}
		if v, ok := m["scope"]; ok {
			keep["scope"] = v
		}
		if err := o.saveMeta(keep); err != nil {
			return "", err
		}
		o.audit("outline: disconnected by " + user)
		return "disconnected; the API key was deleted (revoke it in Outline too)", nil
	case "hosts":
		hs, ok := req["hosts"].([]any)
		if !ok || len(hs) > 10 {
			return "", Refused("up to 10 hosts")
		}
		var clean []string
		seen := map[string]bool{}
		for _, h := range hs {
			s := lower(fmt.Sprint(h))
			if !HostOK(s) {
				return "", Refused("not a host name: " + head(s, 60))
			}
			if !seen[s] {
				seen[s] = true
				clean = append(clean, s)
			}
		}
		text := ""
		for _, h := range clean {
			text += h + "\n"
		}
		if err := writeFile(o.path("outline-allowed-hosts"), []byte(text), 0o644); err != nil {
			return "", err
		}
		o.audit(fmt.Sprintf("outline: allowed hosts set by %s: %s", user, orS(strings.Join(clean, ", "), "(none)")))
		return "saved", nil
	}
	return "", Refused("unknown request kind")
}

func ifS(b bool, s string) string {
	if b {
		return s
	}
	return ""
}

func head(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Result is one request's result in the status.
type Result struct {
	User   string `json:"user"`
	Kind   string `json:"kind"`
	Result string `json:"result"`
	At     int64  `json:"at"`
}

// Requests applies every pending request and writes the status; the number applied.
func (o *Options) Requests() (int, error) {
	o.defaults()
	names, _ := os.ReadDir(o.ReqDir)
	var list []string
	for _, n := range names {
		list = append(list, n.Name())
	}
	sort.Strings(list)
	results := map[string]Result{}
	for _, name := range list {
		path := filepath.Join(o.ReqDir, name)
		m := nameRe.FindStringSubmatch(name)
		if m == nil {
			if !strings.HasPrefix(name, ".") {
				os.Remove(path)
			}
			continue
		}
		user, rid := m[1], m[2]
		req, err := o.readRequest(path)
		token := ""
		var res string
		if err == nil {
			token = str(req["token"])
			switch {
			case str(req["user"]) != user:
				err = Refused("request user does not match its file name")
			default:
				role := o.userRole(user)
				if role == "" {
					err = Refused("unknown or disabled user")
				} else {
					res, err = o.handle(req, user, role)
				}
			}
		}
		var r Refused
		switch {
		case errors.As(err, &r):
			res = "refused: " + string(r)
		case err != nil:
			res = "refused: could not save (" + err.Error() + ")"
		}
		if token != "" {
			res = strings.ReplaceAll(res, token, "[REDACTED]")
		}
		results[rid] = Result{User: user, Kind: head(str(req["kind"]), 20), Result: head(res, 400), At: o.now()}
		fmt.Fprintf(o.Log, "outline request %s %s by %s: %s\n", rid, orS(str(req["kind"]), "?"), user, head(res, 200))
	}
	return len(results), o.writeStatus(results, nil)
}

// --- publish -------------------------------------------------------------------------------------------------

// Page is one rendered page (ops.db outline_pages).
type Page struct {
	Service, Title, Body, Hash, Blocked, DocID string
}

// PageState is the publish state of one page.
type PageState struct {
	DocID       string   `json:"doc_id,omitempty"`
	URL         string   `json:"url,omitempty"`
	State       string   `json:"state,omitempty"`
	Error       string   `json:"error"`
	Flags       []string `json:"flags"`
	Known       []string `json:"known,omitempty"`
	CheckedAt   int64    `json:"checked_at,omitempty"`
	PublishedAt int64    `json:"published_at,omitempty"`
}

// State is the publish state file.
type State struct {
	Pages map[string]*PageState `json:"pages"`
}

// Run is the summary of one publish run.
type Run struct {
	At     int64          `json:"at"`
	Mode   string         `json:"mode"`
	Counts map[string]int `json:"counts"`
	Error  string         `json:"error"`
	Note   string         `json:"note,omitempty"`
	Parent string         `json:"parent,omitempty"`
	Calls  int            `json:"calls"`
}

func (o *Options) pages(scope string) ([]Page, error) {
	if o.DB == nil {
		return nil, nil
	}
	rs, err := o.DB.Query("SELECT service, title, body, body_hash, COALESCE(blocked,''), COALESCE(doc_id,'') FROM outline_pages ORDER BY service = ?, service", Index)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	pilot := map[string]bool{}
	for _, p := range o.Pilot {
		pilot[p] = true
	}
	var out []Page
	for rs.Next() {
		var p Page
		if err := rs.Scan(&p.Service, &p.Title, &p.Body, &p.Hash, &p.Blocked, &p.DocID); err != nil {
			return nil, err
		}
		if scope == "all" || pilot[p.Service] || p.Service == Index {
			out = append(out, p)
		}
	}
	return out, rs.Err()
}

func (o *Options) known(st *PageState, returned string, ownBlock bool) []string {
	k := append([]string{}, st.Known...)
	if _, b, _, ok := o.Marks.Split(returned); ok && ownBlock {
		k = append(k, BlockHash(b))
	}
	if p, ok := o.Marks.Proposed(returned); ok {
		k = append(k, BlockHash(p))
	}
	var out []string
	seen := map[string]bool{}
	for _, x := range k {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	if len(out) > 6 {
		out = out[len(out)-6:]
	}
	return out
}

// checkParent is the parent's title, or a refusal: missing, trashed, archived or elsewhere means nothing is published.
func (o *Options) checkParent(c *Client, m Meta) (string, error) {
	pid, name := m.s("parent_id"), orS(m.s("parent"), "?")
	if pid == "" {
		return "", Refused("no parent document is configured: reconnect Outline in Settings → Integrations (" + o.Name +
			" never publishes at the collection root); nothing was published")
	}
	d, err := c.Info(pid)
	if err != nil && !notFoundish(err) {
		return "", err
	}
	if err != nil {
		d = map[string]any{}
	}
	if why := usableParent(d, m.s("collection_id")); why != "" {
		return "", Refused(fmt.Sprintf("the parent document %q (%s) cannot be used: %s. Nothing was published (%s never "+
			"falls back to the collection root); restore it in Outline or reconnect to pick another parent", name, pid, why, o.Name))
	}
	return orS(str(d["title"]), name), nil
}

func gone(d map[string]any, m Meta) bool {
	return str(d["deletedAt"]) != "" || str(d["archivedAt"]) != "" ||
		(str(d["collectionId"]) != "" && str(d["collectionId"]) != m.s("collection_id"))
}

func absURL(base, u string) string {
	if strings.HasPrefix(u, "/") {
		return strings.TrimRight(base, "/") + u
	}
	return u
}

func (o *Options) publishPage(c *Client, m Meta, p Page, st *PageState, titles map[string]string) (*PageState, error) {
	title := p.Title
	if p.Service == Index {
		title = o.IndexTitle
	}
	out := *st
	out.Error = ""
	if out.Flags == nil {
		out.Flags = []string{}
	}
	docID := orS(orS(st.DocID, p.DocID), titles[title])
	if docID != "" {
		d, err := c.Info(docID)
		if err != nil {
			if e, ok := asErr(err); ok && (e.Status == 400 || e.Status == 404) {
				d, docID = nil, ""
				out.Flags = []string{"recreated"}
			} else {
				return nil, err
			}
		}
		if d != nil && gone(d, m) {
			d, docID = nil, "" // in the trash, archived or moved to another collection: never written again
			out.Flags = []string{"recreated"}
		}
		if d != nil {
			remote := str(d["text"])
			text, state, flags := o.Marks.Merge(remote, p.Body, st.Known)
			returned, u := remote, str(d["url"])
			if state != "unchanged" {
				r, err := c.Update(docID, text)
				if err != nil {
					return nil, err
				}
				returned = orS(str(r["text"]), text)
				u = orS(str(r["url"]), u)
			}
			out.DocID, out.State, out.CheckedAt = docID, state, o.now()
			if u != "" {
				out.URL = absURL(m.s("url"), u)
			}
			out.Known = o.known(st, returned, (state == "updated" || state == "unchanged") && len(flags) == 0)
			if str(d["parentDocumentId"]) != m.s("parent_id") {
				flags = append(flags, "outside-parent") // a person moved it: kept where it is, shown in the status
			}
			out.Flags = flags
			if state != "unchanged" {
				out.PublishedAt = o.now()
			}
			return &out, nil
		}
	}
	text := o.Marks.NewDocument(p.Body)
	r, err := c.Create(title, text, m.s("collection_id"), m.s("parent_id"))
	if err != nil {
		return nil, err
	}
	if str(r["id"]) == "" {
		return nil, &Error{Msg: "Outline did not return the new document"}
	}
	out.DocID, out.URL, out.State = str(r["id"]), absURL(m.s("url"), str(r["url"])), "created"
	out.Known = o.known(&PageState{}, orS(str(r["text"]), text), true)
	out.PublishedAt, out.CheckedAt = o.now(), o.now()
	return &out, nil
}

// Publish creates or updates every page in scope (publish mode only), under the parent document, and writes the
// state, the status and the doc ids back into outline_pages.
func (o *Options) Publish() (Run, error) {
	o.defaults()
	m := o.meta()
	st := State{Pages: map[string]*PageState{}}
	readJSON(o.StateFile, &st)
	if st.Pages == nil {
		st.Pages = map[string]*PageState{}
	}
	run := o.publish(m, &st)
	b, _ := json.MarshalIndent(st, "", " ")
	if err := writeFile(o.StateFile, b, 0o644); err != nil {
		return run, err
	}
	if err := o.copyBack(&st); err != nil {
		return run, err
	}
	return run, o.writeStatus(nil, &run)
}

func (o *Options) publish(m Meta, st *State) Run {
	run := Run{At: o.now(), Mode: orS(m.s("mode"), "preview"), Counts: map[string]int{}}
	if run.Mode != "publish" {
		run.Note = "preview mode: nothing sent to Outline"
		return run
	}
	pages, err := o.pages(orS(m.s("scope"), "pilot"))
	if err != nil {
		run.Error = head(err.Error(), 300)
		return run
	}
	c, err := o.storedClient(m)
	if err != nil {
		run.Error = err.Error()
		return run
	}
	defer func() { run.Calls = c.Calls }()
	parent, err := o.checkParent(c, m)
	if err != nil {
		run.Error, run.Calls = err.Error(), c.Calls
		return run
	}
	run.Parent = parent
	titles := map[string]string{}
	need := false
	for _, p := range pages {
		if (st.Pages[p.Service] == nil || st.Pages[p.Service].DocID == "") && p.DocID == "" {
			need = true
		}
	}
	if need { // a lost id is found again by title, but only among the parent's own children: never a person's page
		docs, err := c.Documents(m.s("collection_id"))
		if err != nil {
			run.Error, run.Calls = head(err.Error(), 300), c.Calls
			return run
		}
		for _, d := range docs {
			if str(d["id"]) != "" && str(d["parentDocumentId"]) == m.s("parent_id") && !gone(d, m) {
				titles[str(d["title"])] = str(d["id"])
			}
		}
	}
	for _, p := range pages {
		cur := st.Pages[p.Service]
		if cur == nil {
			cur = &PageState{}
		}
		if p.Blocked != "" {
			n := *cur
			n.State, n.Error, n.CheckedAt = "blocked", p.Blocked, o.now()
			st.Pages[p.Service] = &n
			run.Counts["blocked"]++
			continue
		}
		ns, err := o.publishPage(c, m, p, cur, titles)
		if err != nil {
			e, ok := asErr(err)
			switch {
			case ok && e.Budget:
				run.Error, run.Calls = e.Msg+"; the rest goes in the next run", c.Calls
				return run
			case ok && e.Auth:
				run.Error, run.Calls = e.Msg+"; replace the API key in Settings → Integrations", c.Calls
				return run
			}
			n := *cur
			n.State, n.Error, n.CheckedAt = "error", head(err.Error(), 300), o.now()
			ns = &n
		}
		st.Pages[p.Service] = ns
		run.Counts[orS(ns.State, "?")]++
	}
	run.Calls = c.Calls
	return run
}

// copyBack writes the doc ids, URLs and states into outline_pages (the pages and the previews read them).
func (o *Options) copyBack(st *State) error {
	if o.DB == nil {
		return nil
	}
	for svc, p := range st.Pages {
		fl, _ := json.Marshal(p.Flags)
		if _, err := o.DB.Exec("UPDATE outline_pages SET doc_id=COALESCE(NULLIF(?, ''), doc_id), doc_url=COALESCE(NULLIF(?, ''), doc_url), "+
			"publish_status=?, publish_flags=?, published_at=COALESCE(NULLIF(?, 0), published_at), publish_error=? WHERE service=?",
			p.DocID, p.URL, p.State, string(fl), p.PublishedAt, p.Error, svc); err != nil {
			return err
		}
	}
	return nil
}

// --- the status the app reads -----------------------------------------------------------------------------

func (o *Options) writeStatus(results map[string]Result, run *Run) error {
	path := filepath.Join(o.OutDir, "outline-status.json")
	old := map[string]any{}
	readJSON(path, &old)
	res := map[string]any{}
	if r, ok := old["results"].(map[string]any); ok {
		res = r
	}
	for k, v := range results {
		res[k] = v
	}
	if len(res) > 60 {
		var ks []string
		for k := range res {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks[:len(ks)-60] {
			delete(res, k)
		}
	}
	m := o.meta()
	conn := map[string]any{}
	if o.token(m) != "" && m.s("url") != "" {
		for _, k := range []string{"url", "host", "collection", "collection_id", "parent", "parent_id", "user", "team", "connected_at", "by"} {
			if v, ok := m[k]; ok && v != nil && v != "" {
				conn[k] = v
			}
		}
	}
	st := State{}
	readJSON(o.StateFile, &st)
	pages := map[string]any{}
	for svc, p := range st.Pages {
		q := *p
		q.Known = nil
		pages[svc] = q
	}
	var lastRun any = old["last_run"]
	if run != nil {
		lastRun = run
	}
	if lastRun == nil {
		lastRun = map[string]any{}
	}
	doc := map[string]any{"updated_at": o.now(), "connected": len(conn) > 0, "connection": conn,
		"settings":      map[string]any{"mode": orS(m.s("mode"), "preview"), "scope": orS(m.s("scope"), "pilot"), "default_url": o.DefaultURL},
		"allowed_hosts": o.AllowedHosts(), "last_run": lastRun, "pages": pages, "results": res}
	b, _ := json.MarshalIndent(doc, "", " ")
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return err
	}
	return writeFile(path, b, 0o644)
}
