package app

// GET /api/v1/today: the data of the Today page (views_today.go) as JSON, from the same queries and helpers.

import (
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

type apiLink struct {
	Text string `json:"text"`
	Href string `json:"href,omitempty"`
}

type apiTodayItem struct {
	ID            int64     `json:"id"`
	N             int       `json:"n,omitempty"` // its number in today's plan (0 for Later)
	Title         string    `json:"title"`
	Href          string    `json:"href"`
	LinkLabel     string    `json:"linkLabel"`
	DoThis        string    `json:"doThis"`
	WhyNow        string    `json:"whyNow"`
	EstMin        int64     `json:"estMin"`
	EstBasis      string    `json:"estBasis"`
	Status        string    `json:"status"`
	Slot          string    `json:"slot"`
	Area          string    `json:"area"`
	Env           string    `json:"env"`
	EnvShort      string    `json:"envShort,omitempty"` // prod / pre / dev
	NeedsWindow   bool      `json:"needsWindow"`
	NeedsApproval bool      `json:"needsApproval"`
	Carried       int64     `json:"carried"`
	StartedAt     int64     `json:"startedAt,omitempty"`
	Ref           string    `json:"ref"`
	Score         float64   `json:"score"`
	Urgency       float64   `json:"urgency"`
	Impact        float64   `json:"impact"`
	Confidence    float64   `json:"confidence"`
	Details       []apiLink `json:"details"`
	PromptURL     string    `json:"promptUrl,omitempty"`
	LaterReason   string    `json:"laterReason,omitempty"`
	CloseHref     string    `json:"closeHref,omitempty"`
	CloseLabel    string    `json:"closeLabel,omitempty"`
	AutoDone      bool      `json:"autoDone"`
	Note          string    `json:"note,omitempty"`
	ActualMin     int64     `json:"actualMin,omitempty"`
}

type apiBanner struct {
	Text    string `json:"text"`
	Kind    string `json:"kind"`              // ok, warn, fail, info
	Refresh int    `json:"refresh,omitempty"` // seconds until the page should load again (a request being applied)
}

// todayDetailLinks is the item's details list (todayDetails) as data: at most 8, local links only.
func todayDetailLinks(r Row) []apiLink {
	out := []apiLink{}
	v, err := py.ParseJSON([]byte(py.OrS(r.Get("details"), "[]")))
	if err != nil {
		return out
	}
	for _, x := range py.AsList(v) {
		if len(out) >= 8 {
			break
		}
		m := py.AsMap(x)
		if m == nil {
			continue
		}
		t, lk := py.Str(py.Or(m.Get("text"), "")), py.Str(py.Or(m.Get("link"), ""))
		if !strings.HasPrefix(lk, "/") || strings.HasPrefix(lk, "//") {
			lk = ""
		}
		out = append(out, apiLink{Text: t, Href: lk})
	}
	return out
}

func todayItemJSON(r Row, n int) apiTodayItem {
	label := py.OrS(r.Get("link_label"), "Open")
	if label == "Lazy card" {
		label = "Summary"
	}
	it := apiTodayItem{ID: r.I("id"), N: n, Title: r.S("title"), Href: todayLink(r), LinkLabel: label, DoThis: r.S("do_this"),
		WhyNow: r.S("why_now"), EstMin: r.I("est_min"), EstBasis: r.S("est_basis"), Status: r.S("status"), Slot: r.S("slot"),
		Area: r.S("area"), Env: r.S("env"), EnvShort: todayEnvShort[r.S("env")], NeedsWindow: r.I("needs_window") == 1,
		NeedsApproval: r.I("needs_approval") == 1, Carried: r.I("carried"), StartedAt: r.I("started_at"), Ref: r.S("ref"),
		Score: r.F("score"), Urgency: r.F("urgency"), Impact: r.F("impact"), Confidence: r.F("confidence"),
		Details: todayDetailLinks(r), LaterReason: py.OrS(r.Get("later_reason"), ""), AutoDone: r.I("auto_done") == 1,
		Note: py.OrS(r.Get("note"), ""), ActualMin: r.I("actual_min")}
	if p := r.S("prompt_url"); strings.HasPrefix(p, "/rca/prompt.md?") {
		it.PromptURL = p
	}
	if r.I("own_action") == 0 && r.I("auto_done") == 0 && strings.HasPrefix(r.S("close_link"), "/") && !strings.HasPrefix(r.S("close_link"), "//") {
		it.CloseHref, it.CloseLabel = r.S("close_link"), todayCloseLabel(r.S("close_link"))
	}
	return it
}

// todaySentBanner is todaySent as data: the result of the admin's last action (?sent=<action>&at=<time>).
func todaySentBanner(qs py.QS, p Row) *apiBanner {
	act, at := qs.Get("sent"), qs.Get("at")
	if !In(act, todayActions...) || !todayRidRe.MatchString(at) {
		return nil
	}
	word := act
	for _, kv := range strings.Split(todaySentWords, "|") {
		if k, v, _ := strings.Cut(kv, ":"); k == act {
			word = v
		}
	}
	t := py.I(at)
	if p != nil && p.I("updated_at") >= t {
		return &apiBanner{Text: word + ".", Kind: "ok"}
	}
	if NowInt()-t < 30 {
		return &apiBanner{Text: "Saving: Argus applies it in a second; this page refreshes by itself.", Kind: "info", Refresh: 2}
	}
	return &apiBanner{Text: "Not applied yet. The today-actions job may be stopped: check argus-today-actions on " + ARGUS_HOSTNAME + ".", Kind: "warn"}
}

// todayWaitingFor leaves out decisions this user cannot open: a viewer has no proposals in Review, so a link there is a dead end.
func todayWaitingFor(list []map[string]any, sess *Session) []map[string]any {
	out := []map[string]any{}
	for _, w := range list {
		u := py.URLSplit(py.Str(w["href"]))
		key := ""
		if u.Path == "/review" {
			key = py.ParseQS(u.Query).Get("kind")
		}
		hidden := false
		for i := range REVIEW_KINDS {
			if k := &REVIEW_KINDS[i]; k.Key == key && !reviewVisible(k, sess) {
				hidden = true
			}
		}
		if !hidden {
			out = append(out, w)
		}
	}
	return out
}

// todayWaitingList is the planner's snapshot of open decisions (todayWaiting) as data.
func todayWaitingList(st *py.OMap) []map[string]any {
	out := []map[string]any{}
	l, _ := st.Get("decisions").([]any)
	for _, x := range l {
		m := py.AsMap(x)
		if m == nil {
			continue
		}
		link := py.Str(py.Or(m.Get("link"), ""))
		if to := RedirectFor(py.URLSplit(link).Path, py.URLSplit(link).Query); to != "" {
			link = to
		}
		if !strings.HasPrefix(link, "/") || strings.HasPrefix(link, "//") {
			link = ""
		}
		out = append(out, map[string]any{"group": py.Str(py.Or(m.Get("group"), "")), "title": py.Str(py.Or(m.Get("title"), "")),
			"href": link, "count": py.I(py.Or(m.Get("count"), int64(0))), "detail": py.Str(py.Or(m.Get("detail"), ""))})
	}
	return out
}

// apiReviewSummary is the Review line on Today: how many decisions wait, how long they take, the most urgent four.
func apiReviewSummary(c *Ctx) map[string]any {
	sess := c.Sess
	var total, mins int64
	kinds := []map[string]any{}
	for i := range REVIEW_KINDS {
		k := &REVIEW_KINDS[i]
		if !reviewVisible(k, sess) {
			continue
		}
		n := reviewCount(k, sess)
		total += n
		mins += n * k.Min
		if n > 0 {
			kinds = append(kinds, map[string]any{"key": k.Key, "label": k.Label, "row": k.Row, "count": n})
		}
	}
	all := reviewAll(c)
	reviewSort(all, "urgency")
	top := []apiReviewRow{}
	for i := 0; i < len(all) && i < 4; i++ {
		top = append(top, reviewRowJSON(all[i]))
	}
	return map[string]any{"total": total, "estMin": mins, "kinds": kinds, "top": top}
}

// apiTodayJSON is GET /api/v1/today (?sent=&at= after an action: the banner).
func apiTodayJSON(qs py.QS, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/today"}
	review := apiReviewSummary(c)
	incidents, checks := apiTodayIncidents(), apiTodayChecks()
	if !TodayOn() {
		return map[string]any{"on": false, "review": review, "incidents": incidents, "checks": checks}, nil
	}
	canAct := sess.Role == "admin" && TodayCanAct()
	st := todayStatus()
	p := todayPlan()
	out := map[string]any{"on": true, "canAct": canAct, "admin": sess.Role == "admin", "review": review, "incidents": incidents, "checks": checks,
		"waiting": todayWaitingFor(todayWaitingList(st), sess), "planAt": py.OrS(st.Get("plan_at"), "07:30"), "plan": nil}
	if p == nil {
		return out, nil
	}
	items := todayItems(p.I("id"))
	num := map[int64]int{}
	n := 0
	for _, r := range items {
		if r.S("slot") == "today" && In(r.S("status"), "planned", "doing", "done") {
			n++
			num[r.I("id")] = n
		}
	}
	open, done, later := []apiTodayItem{}, []apiTodayItem{}, []apiTodayItem{}
	var planned, doneMin, actual int64
	for _, r := range items {
		switch {
		case r.S("slot") == "today" && r.S("status") == "done":
			done = append(done, todayItemJSON(r, num[r.I("id")]))
			planned += r.I("est_min")
			doneMin += r.I("est_min")
			actual += r.I("actual_min")
		case r.S("slot") == "today" && In(r.S("status"), "planned", "doing"):
			open = append(open, todayItemJSON(r, num[r.I("id")]))
			planned += r.I("est_min")
		default:
			later = append(later, todayItemJSON(r, 0))
		}
	}
	day := p.S("day")
	dayLabel := day
	if t, err := time.Parse("2006-01-02", day); err == nil {
		dayLabel = t.Format("Mon 2 Jan")
	}
	out["plan"] = map[string]any{"id": p.I("id"), "day": day, "dayLabel": dayLabel,
		"isToday": day == LocalTime(Now()).Format("2006-01-02"), "budgetMin": p.I("budget_min"), "maxItems": p.I("max_items"),
		"maxRisky": p.I("max_risky"), "plannedMin": planned, "doneMin": doneMin, "actualMin": actual, "updatedAt": p.I("updated_at")}
	out["open"], out["done"], out["later"] = open, done, later
	if b := todaySentBanner(qs, p); b != nil {
		out["banner"] = b
	}
	return out, nil
}
