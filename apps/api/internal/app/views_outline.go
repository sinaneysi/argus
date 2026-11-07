package app

// views_outline.py: Settings -> Integrations: Outline service documentation and the page previews with
// their diffs. The API key is written once into settings-in/outline-requests/<user>-<16 hex>.json (0600) and
// forgotten; this module reads only settings-out/outline-status.json and ops.db outline_pages.

import (
	"path/filepath"
	"regexp"

	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var (
	outlineTokenRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{20,200}\n?$`)
	outlineHostRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*(:[0-9]{1,5})?\n?$`)
	outlineSvcRe   = regexp.MustCompile(`^(?:_index|[a-z0-9][a-z0-9._-]{0,40}/[a-z0-9][a-z0-9._-]{0,80})\n?$`)

	outlineQuoteRe = regexp.MustCompile(`(?m)^> ?`)
	outlineBadColl = regexp.MustCompile(`[\x00-\x1f<>]`)
	// OutlineTestAllowHTTP: tests only (a fake Outline on http://127.0.0.1).
	OutlineTestAllowHTTP = false
)

var outlineDefaultURL = OUTLINE_URL

var outlineStateLabel = map[string]string{"created": "created", "updated": "updated", "unchanged": "up to date",
	"human-edit": "human edit kept", "no-markers": "markers removed", "blocked": "blocked", "error": "error"}
var outlineStateKind = map[string]string{"created": "ok", "updated": "ok", "unchanged": "ok", "human-edit": "warn",
	"no-markers": "warn", "blocked": "fail", "error": "fail"}

func outlineStatus() *py.OMap { return ReadJSONMap(filepath.Join(SET_OUT, "outline-status.json")) }

func outlinePages() []Row {
	return Q("SELECT service, title, body_hash, prev_hash, rendered_at, changed_at, blocked, redacted, " +
		"summary, doc_id, doc_url, publish_status, publish_flags FROM outline_pages " +
		"ORDER BY service = '_index' DESC, service")
}

func outlineDrop(sess *Session, payload *py.OMap) (string, error) {
	d := filepath.Join(SET_IN, "outline-requests")
	rid := auth.TokenHex(8)
	name := sess.Username + "-" + rid + ".json"
	p := payload.Copy()
	p.Set("user", sess.Username)
	p.Set("at", NowInt())
	if err := boardDropExcl(d, name, []byte(py.Dumps(p, -1))); err != nil {
		return "", err
	}
	logf("outline %s requested by %s: %s", py.Str(payload.Get("kind")), sess.Username, rid)
	return rid, nil
}

func outlineResult(c *Ctx, rid, user string) string {
	if !jiraRidRe.MatchString(rid) {
		return ""
	}
	if boardExists(filepath.Join(SET_IN, "outline-requests", user+"-"+rid+".json")) {
		c.Refresh = true
		return Banner("Working on "+ARGUS_HOSTNAME+"; this page refreshes itself.", "ok")
	}
	r := py.AsMap(py.AsMap(outlineStatus().Get("results")).Get(rid))
	if !py.Truthy(r) || r.Get("user") != user {
		c.Refresh = true
		return StateBox(StSynced, "Saved. Waiting for the Outline job to apply it.", "Safe to leave this page: the result appears here once it has run. The page refreshes itself.", "")
	}
	res := py.Str(r.GetD("result", ""))
	if strings.HasPrefix(res, "refused: ") {
		return StateBox(StFailed, "Not saved.", "The sync refused it: "+SafeErr(py.Slice(res, 9))+". Nothing was changed; correct it and save again.", "")
	}
	return Banner(strings.ToUpper(py.Head(res, 1))+py.Slice(res, 1)+".", "ok")
}

// sameVal is Python's == for two SQLite values (1 == 1.0, None == None).
func sameVal(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if py.IsNumber(a) && py.IsNumber(b) {
		return py.Num(a) == py.Num(b)
	}
	return a == b
}

// OutlinePageAdminRejected is Settings → Integrations again after a refused save: the entries kept, the field marked.
func OutlineRefused(c *Ctx, rej *Rejected) string {
	c.Rej = rej
	return rej.Msg
}

// outlineDiffLines is the diff as lines, each with its kind (add, del, hunk or ""), and how many lines were left out.
func outlineDiffLines(oldS, newS string) ([]map[string]string, int) {
	lines := outlineUnifiedDiff(py.SplitLines(oldS), py.SplitLines(newS), "before", "now", 2)
	out := []map[string]string{}
	if len(lines) == 0 {
		return out, 0
	}
	body := lines[2:]
	if len(body) > 600 {
		body = body[:600]
	}
	for _, ln := range body {
		kind := ""
		switch {
		case strings.HasPrefix(ln, "+"):
			kind = "add"
		case strings.HasPrefix(ln, "-"):
			kind = "del"
		case strings.HasPrefix(ln, "@@"):
			kind = "hunk"
		}
		out = append(out, map[string]string{"kind": kind, "text": ln})
	}
	return out, max(0, len(lines)-602)
}

// OutlinePost is views_outline.post(form, sess): (303, location) or (code, html).
func OutlinePost(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	get := form.Get
	act := get("action")
	var rid string
	var err error
	switch act {
	case "connect":
		url := py.Strip(get("url"))
		if url == "" {
			url = outlineDefaultURL
		}
		token := py.Strip(get("token"))
		coll := py.Strip(get("collection"))
		if coll == "" {
			coll = "Services"
		}
		okURL := (strings.HasPrefix(url, "https://") || (OutlineTestAllowHTTP && strings.HasPrefix(url, "http://"))) && py.Len(url) <= 300
		for _, r := range url {
			if py.IsSpaceRune(r) {
				okURL = false
			}
		}
		if !okURL {
			return 400, OutlineRefused(c, Reject("outline-connect", "url", "The Outline URL must start with https:// (at most 300 characters, no spaces).", form))
		}
		if !outlineTokenRe.MatchString(token) {
			return 400, OutlineRefused(c, Reject("outline-connect", "token", "That does not look like an Outline API key (Outline: Settings → API → New API key).", form))
		}
		if py.Len(coll) > 100 || outlineBadColl.MatchString(coll) {
			return 400, OutlineRefused(c, Reject("outline-connect", "collection", "Check the collection name: at most 100 characters, no < or >.", form))
		}
		rid, err = outlineDrop(sess, py.NewOMap("kind", "connect", "url", url, "token", token, "collection", coll,
			"create_collection", get("create_collection") == "1"))
	case "settings":
		mode, scope := get("mode"), get("scope")
		if mode == "" {
			mode = "preview"
		}
		if scope == "" {
			scope = "pilot"
		}
		if !In(mode, "preview", "publish") {
			return 400, OutlineRefused(c, Reject("outline-settings", "mode", "Choose a mode: preview or publish.", form))
		}
		if !In(scope, "pilot", "all") {
			return 400, OutlineRefused(c, Reject("outline-settings", "scope", "Choose the services: pilot or all.", form))
		}
		rid, err = outlineDrop(sess, py.NewOMap("kind", "settings", "mode", mode, "scope", scope))
	case "sync", "disconnect":
		rid, err = outlineDrop(sess, py.NewOMap("kind", act))
	case "hosts":
		hosts, bad := boardSplitHosts(get("hosts"), outlineHostRe)
		if len(bad) > 0 || len(hosts) > 10 {
			b := "too many"
			if len(bad) > 0 {
				b = bad[0]
			}
			msg := "Not a host name: " + b + ". One host per line, optionally host:port; no scheme or path."
			if len(bad) == 0 {
				msg = "At most 10 hosts, one per line."
			}
			return 400, OutlineRefused(c, Reject("outline-hosts", "hosts", msg, form))
		}
		hl := make([]any, len(hosts))
		for i, h := range hosts {
			hl[i] = h
		}
		rid, err = outlineDrop(sess, py.NewOMap("kind", "hosts", "hosts", hl))
	default:
		return 400, "Unknown action"
	}
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/admin/outline?r=" + rid
}
