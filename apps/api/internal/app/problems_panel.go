package app

// The "Problem" panel on issue and RCA pages (HLD §5.1), the bulk "Link selected to…" bar of the Incidents and QA
// lists, and POST /problems/act: every control writes one JSON request to settings-in/problem-actions/; bin/problems.py
// (argus) validates and applies it. No JavaScript: the problem search is a GET form that re-renders the page.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

const (
	probMaxRefs  = 50
	probMaxReq   = 4096
	probMaxTitle = 200
	probMaxNote  = 300
)

var probReqRe = regexp.MustCompile(`^[0-9]{10}-[0-9a-f]{8}$`)
var probUserRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,39}$`)

// probBack is the page's own URL without the panel's transient parameters (sent, preq, pq, all).
func probBack(c *Ctx, qs py.QS) string {
	path := py.URLSplit(c.Path).Path
	if path == "" {
		path = "/"
	}
	keep := qs.Pairs("sent", "preq", "pq", "all")
	if len(keep) > 0 {
		path += "?" + py.Urlencode(keep)
	}
	return path
}

// probSentBanner says what happened to the request this page just sent (?sent=problem&preq=<rid>).
func probSentBanner(c *Ctx, qs py.QS) string {
	if qs.Get("sent") != "problem" {
		return ""
	}
	if rid := qs.Get("preq"); probReqRe.MatchString(rid) {
		if _, err := os.Stat(filepath.Join(SET_IN, "problem-actions", rid+".json")); err == nil {
			c.Refresh = true
			return Banner("Sent. Argus applies it within seconds; this page refreshes itself.")
		}
	}
	return Banner("Applied. A request Argus refused changes nothing; the problem's timeline shows what was done.")
}

func probHidden(sess *Session, kv ...string) string {
	out := `<input type="hidden" name="csrf" value="` + sess.CSRF + `">`
	for i := 0; i+1 < len(kv); i += 2 {
		out += `<input type="hidden" name="` + kv[i] + `" value="` + E(kv[i+1]) + `">`
	}
	return out
}

// --- bulk link from the Incidents and QA lists ----------------------------------------------------------------

// ProblemBulkOn: the list shows row checkboxes and the bulk bar (admins, feature on, request dir present).
func ProblemBulkOn(c *Ctx) bool {
	return c.Sess != nil && c.Sess.Role == "admin" && ProblemsOn() && ProblemsCanAct()
}

// --- POST /problems/act ------------------------------------------------------------------------------------------

func probFormInt(v string) (int64, bool) {
	v = strings.TrimPrefix(strings.TrimPrefix(py.Strip(v), "P-"), "p-")
	if !py.IsDigit(v) || len(v) > 9 {
		return 0, false
	}
	n := py.I(v)
	return n, n > 0
}

func probFormIDs(vals []string) ([]any, bool) {
	if len(vals) == 0 || len(vals) > probMaxRefs {
		return nil, false
	}
	seen := map[int64]bool{}
	var out []any
	for _, v := range vals {
		n, ok := probFormInt(v)
		if !ok {
			return nil, false
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, true
}

func probText(v string, max int) string {
	return py.Head(strings.Join(py.Split(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, v)), " "), max)
}

// ProblemsPostAct is POST /problems/act: (status, Location or message).
func ProblemsPostAct(c *Ctx, form py.QS, sess *Session) (int, string) {
	get := form.Get
	if !ProblemsOn() {
		return 404, "Problem records are off."
	}
	if !ProblemsCanAct() {
		return 409, "Problem actions are not set up on this host yet (settings-in/problem-actions is missing)."
	}
	if !probUserRe.MatchString(sess.Username) {
		return 403, "Your user name cannot sign a request."
	}
	act := get("action")
	admin := sess.Role == "admin"
	if act != "link" && !admin {
		return 403, "Only admins change problems; viewers can suggest a link."
	}
	req := py.NewOMap("action", act, "by", sess.Username, "role", sess.Role, "at", NowInt())
	pidFor := ""
	switch act {
	case "create":
		title := probText(get("title"), probMaxTitle)
		if py.Len(title) < 3 {
			return 400, "A problem needs a title of at least 3 characters."
		}
		req.Set("title", title)
		if f := get("from"); f != "" {
			t, _, ok := probParseRef(f)
			if !ok || !In(t, append(probIssueTypes, "rca")...) {
				return 400, "A problem is created from an issue or an RCA."
			}
			req.Set("from", f)
		}
		req.Set("note", probText(get("note"), probMaxNote))
	case "link":
		pid, ok := probFormInt(get("pnum"))
		if !ok {
			pid, ok = probFormInt(get("problem"))
		}
		if !ok {
			return 400, "Choose a problem (or type its P- number)."
		}
		if len(Q("SELECT 1 FROM problems WHERE id=?", pid)) == 0 {
			return 404, "No such problem."
		}
		refs := form.List("ref")
		if len(refs) == 0 || len(refs) > probMaxRefs {
			return 400, "Select between 1 and 50 items to link."
		}
		lt := py.OrS(get("link_type"), "caused_by")
		var rl []any
		seen := map[string]bool{}
		for _, r := range refs {
			t, _, ok := probParseRef(r)
			if !ok {
				return 400, "Unknown item."
			}
			want := lt
			if t == "rca" && lt == "caused_by" {
				want = "explains"
			}
			if !probLinkAllowed(t, want) {
				return 400, "A " + probTypeWord[t] + " cannot be linked as " + probLinkWord[lt] + "."
			}
			if !seen[r] {
				seen[r] = true
				rl = append(rl, r)
			}
		}
		req.Set("problem", pid).Set("refs", rl).Set("link_type", lt).Set("note", probText(get("note"), probMaxNote))
		pidFor = I2S(pid)
	case "unlink", "accept", "reject":
		ids, ok := probFormIDs(form.List("link"))
		if !ok {
			return 400, "Select between 1 and 50 links."
		}
		req.Set("links", ids)
		note := probText(get("note"), probMaxNote)
		if act == "unlink" && py.Len(note) < 3 {
			return 400, "Unlinking needs a reason (3 to 300 characters)."
		}
		if act != "accept" {
			req.Set("note", note)
		}
	case "primary":
		lid, ok := probFormInt(get("link"))
		if !ok {
			return 400, "Choose a link."
		}
		req.Set("link", lid)
	case "status", "edit", "merge", "resolve":
		pid, ok := probFormInt(get("problem"))
		if !ok || len(Q("SELECT 1 FROM problems WHERE id=?", pid)) == 0 {
			return 404, "No such problem."
		}
		req.Set("problem", pid)
		pidFor = I2S(pid)
		switch act {
		case "status":
			to := get("to")
			if !In(to, probStatuses[1:]...) {
				return 400, "Choose a status."
			}
			lbl := get("root_cause_label")
			if !In(lbl, "", "proven", "probable", "unproven") {
				return 400, "Unknown proof label."
			}
			rc := probText(get("root_cause"), 400)
			if to == "known_error" && rc == "" && len(Q("SELECT 1 FROM problems WHERE id=? AND COALESCE(root_cause,'')<>''", pid)) == 0 {
				return 400, "A known error needs its root cause."
			}
			if to == "resolved" && py.Len(probText(get("note"), probMaxNote)) < 3 {
				return 400, "Resolving needs a note (3 to 300 characters)."
			}
			req.Set("to", to).Set("note", probText(get("note"), probMaxNote)).Set("root_cause", rc).
				Set("workaround", probText(get("workaround"), 600)).Set("root_cause_label", lbl)
		case "edit":
			changed := false
			if form.Has("owner") {
				o := py.Strip(get("owner"))
				if o != "" && !probUserRe.MatchString(o) {
					return 400, "An owner is a user name (lower case)."
				}
				req.Set("owner", o)
				changed = true
			}
			if form.Has("title") {
				t := probText(get("title"), probMaxTitle)
				if py.Len(t) < 3 {
					return 400, "A problem needs a title of at least 3 characters."
				}
				req.Set("title", t)
				changed = true
			}
			if !changed {
				return 400, "Nothing to change."
			}
		case "merge":
			into, ok := probFormInt(get("into"))
			if !ok || into == pid || len(Q("SELECT 1 FROM problems WHERE id=?", into)) == 0 {
				return 400, "Merge into another existing problem (its P- number)."
			}
			req.Set("into", into)
		case "resolve":
			note := probText(get("note"), probMaxNote)
			if py.Len(note) < 3 {
				return 400, "Resolving needs a note (3 to 300 characters)."
			}
			var iss []any
			if v := form.List("issue"); len(v) > 0 {
				ids, ok := probFormIDs(v)
				if !ok {
					return 400, "Unknown incident."
				}
				iss = ids
			}
			if iss == nil {
				iss = []any{}
			}
			req.Set("note", note).Set("issues", iss)
		}
	default:
		return 400, "Unknown action."
	}
	body := py.Dumps(req, -1)
	if len(body) > probMaxReq {
		return 400, "Too much at once: select fewer items."
	}
	d := filepath.Join(SET_IN, "problem-actions")
	rid := I2S(NowInt()) + "-" + auth.TokenHex(4)
	if err := WriteFileAtomic(filepath.Join(d, rid+".json"), filepath.Join(d, "."+rid+".tmp"), []byte(body)); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	logf("problem %s requested by %s: %s", act, sess.Username, rid)
	back := get("back")
	if back == "" || SafeNext(back) != back {
		back = "/problems"
		if pidFor != "" {
			back = "/problems/view?id=" + pidFor
		}
	}
	frag := ""
	if i := strings.IndexByte(back, '#'); i >= 0 {
		back, frag = back[:i], back[i:]
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	return 303, back + sep + "sent=problem&preq=" + rid + frag
}
