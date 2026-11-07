package app

// Lessons (ADR-0013 decision 4, phase P4): what Argus learned from its runs, reviewed by an admin.
//   /admin/lessons  admins: approve, edit + approve, reject, retire, back to review; the on/off switch. Every action
//                   is a board request (kind "lesson" / "lesson-settings") that root's bin/lessons.py re-validates,
//                   secret-scans and audits; this page only reads board.db.
//   /knowledge      every signed-in user: the approved lessons as one generated, read-only page (help style).
// Only approved lessons ever reach a prompt (bin/lessons.py select); nothing here approves by itself.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

const (
	lessonTitleMax = 120
	lessonBodyMax  = 1500
)

var (
	lessonViews     = [][2]string{{"proposed", "Waiting"}, {"approved", "Approved"}, {"rejected", "Rejected"}, {"retired", "Retired"}}
	lessonEnvs      = []string{"", "develop", "pre-release", "release", "none"}
	lessonKinds     = []string{"", "investigate", "documentation", "change", "production", "code", "unclear", "incident", "general"}
	lessonServiceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}(/[A-Za-z0-9][A-Za-z0-9._-]{0,99}){0,5}$`)
	lessonTagRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)
	lessonJiraKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}-[0-9]{1,9}$`)
	lessonGlKeyRe   = regexp.MustCompile(`^gl-([0-9]{1,9})$`)
)

// lessonsEnabled is board.db state lessons_settings {"enabled": bool}; on when unset.
func lessonsEnabled() bool {
	r := BoardBQ("SELECT v FROM state WHERE k='lessons_settings'")
	if len(r) == 0 {
		return true
	}
	var d map[string]any
	if json.Unmarshal([]byte(r[0].Str("v")), &d) != nil {
		return true
	}
	b, ok := d["enabled"].(bool)
	return !ok || b
}

// lessonSource links the task a lesson came from (Jira key or gl-<task id>), else "-".
func lessonSource(key string) string {
	if m := lessonGlKeyRe.FindStringSubmatch(key); m != nil {
		return `<a href="/board/task?id=` + m[1] + `">` + E(key) + `</a>`
	}
	if lessonJiraKeyRe.MatchString(key) {
		return `<a href="/board/task?key=` + E(key) + `">` + E(key) + `</a>`
	}
	return `<span class="muted">-</span>`
}

func lessonTags(r Row) []string {
	var t []string
	_ = json.Unmarshal([]byte(r.Str("tags")), &t)
	return t
}

func lessonText(s string) string { return strings.ReplaceAll(E(s), "\n", "<br>") }

func lessonHidden(sess *Session, ret, act string, id int64) string {
	return `<input type="hidden" name="csrf" value="` + sess.CSRF + `">` + boardReturnInput(ret) +
		`<input type="hidden" name="action" value="` + act + `"><input type="hidden" name="id" value="` + I2S(id) + `">`
}

func lessonButton(sess *Session, ret, act string, id int64, label, cls string) string {
	return `<form method="post" action="/admin/lessons/act" class="inline">` + lessonHidden(sess, ret, act, id) +
		`<button class="` + cls + `">` + label + `</button></form>`
}

// lessonReviews: why a waiting lesson is waiting (the review column root sets).
var lessonReviews = [][2]string{{"new", "New"}, {"update", "Update of a lesson"}, {"retire?", "Back for review"}}

// lessonsPage is /admin/lessons as a queue page (DESIGN.md "Queue page", in-memory lists): the state tabs with their
// counts, search over title, lesson, service, tags and source, facets Review (Waiting only), Environment and Kind,
// sort per tab (Waiting newest first, Approved by service, the others by decision), 25 a page, then the switch.
// rej is a refused edit of one lesson (its fold opens with the entries kept), or nil.
// lessonsList is the data of the page: the chosen view, its filters and sort, the rows and the counts.
type lessonsList struct {
	l                      listQ
	view                   string
	rows                   []Row
	counts                 map[string]int
	stN, revN, envN, kindN map[string]int
}

func lessonsQuery(qs py.QS) lessonsList {
	view := qs.GetD("view", "proposed")
	if !In(view, "proposed", "approved", "rejected", "retired") {
		view = "proposed"
	}
	defSort := map[string]string{"proposed": "newest", "approved": "service"}[view]
	if defSort == "" {
		defSort = "decided"
	}
	l := newListQ("/admin/lessons", qs, []string{"view", "q", "review", "env", "kind", "sort"},
		map[string]string{"view": "proposed", "sort": defSort}, func(k, v string) bool {
			switch k {
			case "view":
				return In(v, "proposed", "approved", "rejected", "retired")
			case "review":
				return In(v, "new", "update", "retire?")
			case "env":
				return v != "" && In(v, lessonEnvs...)
			case "kind":
				return v != "" && In(v, lessonKinds...)
			case "sort":
				return In(v, "newest", "oldest", "service", "decided", "used")
			}
			return false
		})
	l.tabs = []string{"view"}
	all := BoardBQ("SELECT * FROM lessons ORDER BY proposed_at DESC, id DESC LIMIT 5000")
	words := l.words()
	match := func(r Row, skip string) bool {
		if skip != "view" && r.Str("state") != view {
			return false
		}
		if skip != "review" && view == "proposed" && l.get("review") != "" && r.Str("review") != l.get("review") {
			return false
		}
		if skip != "env" && l.get("env") != "" && r.Str("env") != l.get("env") {
			return false
		}
		if skip != "kind" && l.get("kind") != "" && r.Str("kind") != l.get("kind") {
			return false
		}
		return listMatch(words, r.Str("title"), r.Str("body"), r.Str("service"), strings.Join(lessonTags(r), " "), r.Str("source_key"))
	}
	stN, revN, envN, kindN := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	counts := map[string]int{}
	var rows []Row
	for _, r := range all {
		counts[r.Str("state")]++
		if match(r, "view") {
			stN[r.Str("state")]++
		}
		if match(r, "review") {
			revN[r.Str("review")]++
		}
		if match(r, "env") {
			envN[r.Str("env")]++
		}
		if match(r, "kind") {
			kindN[r.Str("kind")]++
		}
		if match(r, "") {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch l.get("sort") {
		case "oldest":
			return a.I("proposed_at") < b.I("proposed_at")
		case "service":
			if a.Str("service") != b.Str("service") {
				return a.Str("service") < b.Str("service")
			}
			return a.Str("title") < b.Str("title")
		case "decided":
			return a.I("decided_at") > b.I("decided_at")
		case "used":
			return a.I("uses") > b.I("uses")
		}
		return false // newest: the query's order
	})
	return lessonsList{l, view, rows, counts, stN, revN, envN, kindN}
}

// LessonsPost is POST /admin/lessons/act and /admin/lessons/settings (admins; CSRF and 2FA checked before it).
func LessonsPost(c *Ctx, path string, form py.QS, sess *Session) (int, string) {
	get := form.Get
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	bad := func(msg string) (int, string) { return 400, msg }
	rejected := func(_, msg string) (int, string) { return 400, "Not saved: " + msg } // an edit the server refused
	var payload *py.OMap
	switch path {
	case "/admin/lessons/settings":
		payload = py.NewOMap("kind", "lesson-settings", "enabled", get("enabled") == "1")
	case "/admin/lessons/act":
		act, id := get("action"), get("id")
		if !In(act, "approve", "edit", "reject", "retire", "review") || !py.IsDigit(id) || len(id) > 9 || py.I(id) <= 0 {
			return bad("Choose a lesson and an action")
		}
		payload = py.NewOMap("kind", "lesson", "action", act, "lesson_id", py.I(id)) // "id" is the request id
		if act == "edit" {
			title := strings.Join(py.Split(get("title")), " ")
			body := strings.TrimSpace(strings.ReplaceAll(get("body"), "\r", ""))
			svc, env, kind := strings.TrimSpace(get("service")), get("env"), get("kind")
			switch {
			case title == "" || len([]rune(title)) > lessonTitleMax:
				return rejected("title", "Title must be 1 to 120 characters.")
			case body == "" || len([]rune(body)) > lessonBodyMax:
				return rejected("body", "Lesson must be 1 to 1500 characters.")
			case svc != "" && !lessonServiceRe.MatchString(svc):
				return rejected("service", "Service: a GitLab path such as group/project.")
			case !In(env, lessonEnvs...):
				return rejected("env", "Choose an environment from the list.")
			case !In(kind, lessonKinds...):
				return rejected("kind", "Choose a kind from the list.")
			}
			tags := py.Split(strings.ToLower(get("tags")))
			if len(tags) > 6 {
				return rejected("tags", "Up to 6 tags.")
			}
			ta := []any{}
			for _, t := range tags {
				if !lessonTagRe.MatchString(t) {
					return rejected("tags", "Tags: short lowercase words (letters, digits, dot, underscore, dash).")
				}
				ta = append(ta, t)
			}
			payload.Set("lesson", py.NewOMap("title", title, "body", body, "service", svc, "env", env, "kind", kind, "tags", ta))
		}
	default:
		return 404, "Not found"
	}
	rid, err := BoardWriteRequest(payload, sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "", "/review?kind=lessons&req="+rid)
}

// KnowledgeMarkdown is the generated "what Argus knows" document: approved lessons only, grouped by service
// (the same content root writes to /var/lib/argus/knowledge/lessons.md).
func KnowledgeMarkdown(rows []Row) string {
	var b strings.Builder
	cur := "\x00"
	for _, r := range rows {
		svc := r.Str("service")
		if svc != cur {
			cur = svc
			b.WriteString("## " + If(svc == "", "General", svc) + "\n\n")
		}
		meta := []string{}
		for _, k := range []string{"env", "kind"} {
			if v := r.Str(k); v != "" {
				meta = append(meta, v)
			}
		}
		b.WriteString("### " + r.Str("title") + "\n\n")
		if len(meta) > 0 {
			b.WriteString("_" + strings.Join(meta, ", ") + "_ · lesson " + I2S(r.I("id")) + "\n\n")
		}
		b.WriteString(strings.TrimSpace(r.Str("body")) + "\n\n")
	}
	return b.String()
}
