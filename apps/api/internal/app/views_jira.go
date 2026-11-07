package app

// views_jira.py: connect Jira from the user's own Account page, the "Allowed Jira hosts" list and the shared
// connection in Admin > Jira sync. The token is written once into settings-in/jira-connect/<user>-<16 hex>.json
// (mode 0600, O_EXCL|O_NOFOLLOW) and forgotten: never logged, never echoed into a form, never in an error page.

import (
	"path/filepath"
	"regexp"

	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var (
	jiraTokenRe   = regexp.MustCompile(`^[A-Za-z0-9+/=._-]{10,512}\n?$`)
	jiraProjectRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}\n?$`)
	jiraHostRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*(:[0-9]{1,5})?\n?$`)
	jiraRidRe     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// JiraTestAllowHTTP: tests only (a fake Jira on http://127.0.0.1); root re-checks every URL anyway.
	JiraTestAllowHTTP = false
)

const jiraROReason = "Your Jira connection is read-only. Make the change in Jira, or switch to read and write in Account."

// JiraState is views_jira.state(): settings-out/jira-users.json.
func JiraState() *py.OMap { return ReadJSONMap(filepath.Join(SET_OUT, "jira-users.json")) }

// JiraConn is views_jira.conn(user): the user's connection, or nil.
func JiraConn(user string) *py.OMap {
	return py.AsMap(py.AsMap(JiraState().Get("users")).Get(user))
}

func jiraInUse() bool {
	return py.Truthy(BoardStatus().Get("configured")) || py.Truthy(JiraState().Get("users"))
}

// JiraCanWrite is views_jira.can_write(user): may this user's actions be written to Jira?
func JiraCanWrite(user string) (bool, string) {
	if !jiraInUse() {
		return true, ""
	}
	if c := JiraConn(user); py.Truthy(c) {
		if c.Get("mode") == "rw" {
			return true, ""
		}
		return false, jiraROReason
	}
	if py.Truthy(BoardStatus().Get("configured")) {
		return true, ""
	}
	return false, "Connect Jira in Account (read and write) to change tasks from Argus."
}

// jiraDrop writes one request for root; the token (if any) lives only in this call.
func jiraDrop(sess *Session, payload *py.OMap) (string, error) {
	d := filepath.Join(SET_IN, "jira-connect")
	rid := auth.TokenHex(8)
	name := sess.Username + "-" + rid + ".json"
	p := payload.Copy()
	p.Set("user", sess.Username)
	p.Set("at", NowInt())
	if err := boardDropExcl(d, name, []byte(py.Dumps(p, -1))); err != nil {
		return "", err
	}
	logf("jira-connect %s requested by %s: %s", py.Str(payload.Get("kind")), sess.Username, rid)
	return rid, nil
}

func jiraResult(c *Ctx, rid, user string) string {
	if !jiraRidRe.MatchString(rid) {
		return ""
	}
	if boardExists(filepath.Join(SET_IN, "jira-connect", user+"-"+rid+".json")) {
		c.Refresh = true
		return Banner("Testing the connection on "+ARGUS_HOSTNAME+"; this page refreshes itself.", "ok")
	}
	r := py.AsMap(py.AsMap(JiraState().Get("results")).Get(rid))
	if !py.Truthy(r) || r.Get("user") != user {
		c.Refresh = true
		return StateBox(StSynced, "Saved. Waiting for the board sync to apply it.", "Safe to leave this page: the result appears here once it has run. The page refreshes itself.", "")
	}
	res := py.Str(r.GetD("result", ""))
	if strings.HasPrefix(res, "refused: ") {
		return StateBox(StFailed, "Not saved.", "The sync refused it: "+SafeErr(py.Slice(res, 9))+". Nothing was changed; correct it and save again.", "")
	}
	return Banner(strings.ToUpper(py.Head(res, 1))+py.Slice(res, 1)+".", "ok")
}

func jiraStrList(v any) []string {
	var out []string
	for _, x := range py.AsList(v) {
		out = append(out, py.Str(x))
	}
	return out
}

// jiraAllowedNote says which Jira hosts a personal token may reach and how to get another one.
func jiraAllowedNote(sess *Session, hosts []string) string {
	admin := sess.Role == "admin"
	allowed := "No Jira host is allowed yet."
	if len(hosts) > 0 {
		var hs []string
		for _, h := range hosts {
			hs = append(hs, `<span class="mono">`+E(h)+`</span>`)
		}
		allowed = "Allowed hosts: " + strings.Join(hs, ", ") + "."
	}
	switch {
	case admin:
		allowed += " Saving a new host adds it to the list (logged)."
	case len(hosts) > 0:
		allowed += " Ask an admin if yours is missing."
	default:
		allowed += " An admin must connect first, or add the host."
	}
	return allowed
}

func jiraURLOK(url string) bool {
	if !(strings.HasPrefix(url, "https://") || (JiraTestAllowHTTP && strings.HasPrefix(url, "http://"))) || py.Len(url) > 300 {
		return false
	}
	for _, r := range url {
		if py.IsSpaceRune(r) {
			return false
		}
	}
	return true
}

// jiraRefused is a refused personal connection.
func jiraRefused(c *Ctx, form, field, msg string) (int, string) {
	c.Rej = Reject(form, field, msg, py.QS{})
	return 400, msg
}

// JiraPostAccount is views_jira.post_account(form, sess).
func JiraPostAccount(c *Ctx, form py.QS, sess *Session) (int, string) {
	get := form.Get
	act := get("action")
	var rid string
	var err error
	switch act {
	case "connect":
		url, token, mode := py.Strip(get("url")), py.Strip(get("token")), get("mode")
		if mode == "" {
			mode = "ro"
		}
		if !jiraURLOK(url) {
			return jiraRefused(c, "jira-connect", "url", "The Jira URL must start with https://.")
		}
		if !jiraTokenRe.MatchString(token) {
			return jiraRefused(c, "jira-connect", "token", "That does not look like a Jira personal access token.")
		}
		if mode != "ro" && mode != "rw" {
			return jiraRefused(c, "jira-connect", "mode", "Choose read-only or read and write.")
		}
		rid, err = jiraDrop(sess, py.NewOMap("kind", "connect", "url", url, "token", token, "mode", mode))
	case "disconnect":
		rid, err = jiraDrop(sess, py.NewOMap("kind", "disconnect"))
	case "projects":
		var projects []any
		var plist []string
		for _, p := range form.List("project") {
			if jiraProjectRe.MatchString(p) {
				plist = append(plist, p)
			}
		}
		if len(plist) > 50 {
			plist = plist[:50]
		}
		for _, p := range plist {
			projects = append(projects, p)
		}
		if projects == nil {
			projects = []any{}
		}
		dp, mode := get("default_project"), get("mode")
		if (dp != "" && !In(dp, plist...)) || (mode != "ro" && mode != "rw") {
			return jiraRefused(c, "jira-projects", "default_project", "The project for new tasks must be one of the chosen projects.")
		}
		rid, err = jiraDrop(sess, py.NewOMap("kind", "projects", "projects", projects, "default_project", dp, "mode", mode))
	default:
		return 400, "Unknown action"
	}
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/account?jira=" + rid + "#jira"
}

// JiraPostHosts is views_jira.post_hosts(form, sess).
func JiraPostHosts(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	hosts, bad := boardSplitHosts(form.GetD("hosts", ""), jiraHostRe)
	if len(bad) > 0 || len(hosts) > 20 {
		b := "too many"
		if len(bad) > 0 {
			b = bad[0]
		}
		msg := "Not a host name: " + b + ". One host per line, optionally host:port; no scheme or path."
		if len(bad) == 0 {
			msg = "At most 20 hosts, one per line."
		}
		return 400, BoardRefused(c, Reject("jira-hosts", "hosts", msg, form))
	}
	hl := make([]any, len(hosts))
	for i, h := range hosts {
		hl[i] = h
	}
	rid, err := jiraDrop(sess, py.NewOMap("kind", "hosts", "hosts", hl))
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/admin/board?jira=" + rid + "#hosts"
}

// boardSplitHosts is [h.strip().lower() for h in raw.replace(",", "\n").splitlines() if h.strip()] and the bad ones.
func boardSplitHosts(raw string, re *regexp.Regexp) ([]string, []string) {
	var hosts, bad []string
	for _, h := range py.SplitLines(strings.ReplaceAll(raw, ",", "\n")) {
		if py.Strip(h) == "" {
			continue
		}
		h = strings.ToLower(py.Strip(h))
		hosts = append(hosts, h)
		if !re.MatchString(h) {
			bad = append(bad, h)
		}
	}
	return hosts, bad
}

// jiraSharedKV is the shared connection's facts.
func jiraSharedKV(st, m, bs *py.OMap) [][2]string {
	errS := py.OrS(py.Or(bs.Get("auth_error"), bs.Get("last_error")), "")
	var ver []string
	for _, k := range []string{"version", "deployment"} {
		if py.Truthy(m.Get(k)) {
			ver = append(ver, py.Str(m.Get(k)))
		}
	}
	verS := strings.Join(ver, " ")
	if verS == "" {
		verS = "-"
	}
	project := `<span class="warnc">choose one below</span>`
	if py.Truthy(m.Get("project")) {
		project = `<span class="mono">` + E(py.Str(m.Get("project"))) + `</span>`
	}
	board := `<span class="dim">none (optional)</span>`
	if py.Truthy(m.Get("board_id")) {
		board = `Board ` + E(py.Str(m.Get("board_id"))) + ` — ` + E(py.OrS(m.Get("board_name"), ""))
	}
	comp := E(py.OrS(m.Get("component"), ""))
	if comp == "" {
		comp = `<span class="dim">none</span>`
	}
	mode := "Read-only"
	if m.Get("mode") == "rw" {
		mode = "Read and write"
	}
	ca := `<span class="dim">none (public CA)</span>`
	if py.Truthy(st.Get("ca_installed")) {
		ca = "installed"
	}
	last := `<span class="dim">not yet</span>`
	if py.Truthy(bs.Get("last_ok")) {
		last = When(bs.Get("last_ok"))
	}
	lerr := `<span class="dim">none</span>`
	if errS != "" {
		lerr = `<span class="failc">` + E(errS) + `</span>`
	}
	kv := [][2]string{{"Connected as", E(py.OrS(py.Or(m.Get("display"), m.Get("jira_name")), "?")) + ` ` +
		`<span class="dim small mono">` + E(py.OrS(m.Get("jira_name"), "")) + `</span>`},
		{"Jira", `<span class="mono small">` + E(py.Str(m.Get("url"))) + `</span>`},
		{"Version", E(verS)}, {"Project", project}, {"Board", board}, {"Component", comp}, {"Mode", mode},
		{"Internal CA", ca}, {"Last sync", last}, {"Last error", lerr}}
	return kv
}

// JiraPostShared is views_jira.post_shared(form, sess).
func JiraPostShared(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	get := form.Get
	act := get("action")
	boardID := py.Strip(get("board_id"))
	if boardID == "" {
		boardID = py.Strip(get("board_pick"))
	}
	mode := get("mode")
	if mode == "" {
		mode = "ro"
	}
	fields := py.NewOMap("component", py.Head(py.Strip(get("component")), 80), "project", strings.ToUpper(py.Strip(get("project"))),
		"board_id", boardID, "approvers", py.Strip(get("approvers")), "mode", mode)
	project := fields.S("project")
	boardOK := boardID == "" || py.IsDigit(boardID) || strings.HasPrefix(boardID, "https://") ||
		(JiraTestAllowHTTP && strings.HasPrefix(boardID, "http://"))
	fk := map[string]string{"shared-connect": "jira-connect", "shared-settings": "jira-settings"}[act]
	if fk != "" {
		boardField := "board_id"
		if py.Strip(get("board_id")) == "" && act == "shared-settings" {
			boardField = "board_pick"
		}
		switch {
		case project != "" && !jiraProjectRe.MatchString(project):
			return 400, BoardRefused(c, Reject(fk, "project", "Project key: a letter, then up to 19 letters, digits or underscores.", form))
		case mode != "ro" && mode != "rw":
			return 400, BoardRefused(c, Reject(fk, "mode", "Choose a mode: read-only or read and write.", form))
		case !boardOK || py.Len(boardID) > 300:
			return 400, BoardRefused(c, Reject(fk, boardField, "Board: a number or a board URL of your Jira (https://…).", form))
		}
	}
	var rid string
	var err error
	switch act {
	case "shared-connect":
		url, token := py.Strip(get("url")), py.Strip(get("token"))
		if !jiraURLOK(url) {
			return 400, BoardRefused(c, Reject("jira-connect", "url", "The Jira URL must start with https:// (at most 300 characters).", form))
		}
		if !jiraTokenRe.MatchString(token) {
			return 400, BoardRefused(c, Reject("jira-connect", "token", "That does not look like a Jira personal access token.", form))
		}
		ca := strings.ReplaceAll(get("ca_pem"), "\r\n", "\n")
		if py.Len(ca) > 64*1024 {
			return 400, BoardRefused(c, Reject("jira-connect", "ca_pem", "The CA certificate is too large (64 KB at most).", form))
		}
		p := fields.Copy()
		p.Set("kind", "shared-connect").Set("url", url).Set("token", token).Set("ca_pem", ca)
		rid, err = jiraDrop(sess, p)
	case "shared-settings":
		p := fields.Copy()
		p.Set("kind", "shared-settings")
		rid, err = jiraDrop(sess, p)
	case "shared-disconnect":
		rid, err = jiraDrop(sess, py.NewOMap("kind", "shared-disconnect"))
	default:
		return 400, "Unknown action"
	}
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/admin/board?jira=" + rid + "#shared"
}
