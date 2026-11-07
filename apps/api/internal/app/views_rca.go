package app

// Port of app/views_rca.py: the RCA reports list and page, and the "Investigate deeply" request
// (settings-in/requests/rca-deep-<incident id>.req, re-validated by root).

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

var rcaStatusKind = map[string]string{"merged": "ok", "open": "warn", "local": "warn", "closed": "fail"}

const (
	RcaGap    = 3600
	RcaPerDay = 6
)

func rcaBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// rcaRows is rows(): all RCAs, newest first (DB rows plus merged files the DB does not hold).
func rcaRows() []Row {
	db := Q("SELECT * FROM rca_reports ORDER BY created DESC LIMIT 500")
	have := map[string]bool{}
	var out []Row
	for _, r := range db {
		have[rcaBase(r.S("path"))] = true
		x := Row{}
		for k, v := range r {
			x[k] = v
		}
		x["kind"] = "db"
		out = append(out, x)
	}
	for _, f := range RcaFiles() {
		if have[f] {
			continue
		}
		var mt int64
		if st, err := os.Stat(filepath.Join(REPORTS, f)); err == nil {
			mt = st.ModTime().Unix()
		}
		out = append(out, Row{"kind": "file", "id": nil, "incident_id": nil, "title": py.Slice(f, 0, -3), "path": "reports/rca/" + f,
			"mr_url": nil, "created": mt, "status": "merged", "source": "file", "cost_usd": nil, "duration_s": nil,
			"requested_by": nil})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := py.Num(py.Or(out[i].Get("created"), int64(0))), py.Num(py.Or(out[j].Get("created"), int64(0)))
		if ci != cj {
			return ci > cj
		}
		return out[i].S("path") > out[j].S("path")
	})
	return out
}

func rcaHref(r Row) string {
	if r.Get("kind") == "db" {
		return "/rca/view?id=" + I2S(r.I("id"))
	}
	return "/incidents/rca?f=" + py.Quote(rcaBase(r.S("path")))
}

func rcaCost(r Row) string {
	switch v := r.Get("cost_usd").(type) {
	case int64:
		return "$" + py.F(float64(v), 2)
	case float64:
		return "$" + py.F(v, 2)
	}
	return ""
}

func rcaMins(r Row) string {
	if !r.T("duration_s") {
		return ""
	}
	return " · " + I2S(py.FloorDiv(r.I("duration_s"), 60)) + " min"
}

func rcaQuery(qs py.QS) listQ {
	return newListQ("/rca", qs, []string{"q", "status", "source", "sort"}, map[string]string{"sort": "newest"}, func(k, v string) bool {
		if k == "sort" {
			return In(v, "newest", "oldest", "cost")
		}
		return len(v) <= 40 && proposalLabelRe.MatchString(v)
	})
}

// rcaCompute filters and sorts the reports: the rows shown and the facet counts (the page and GET /api/v1/rca).
func rcaCompute(l listQ, items []Row) ([]Row, map[string]int, map[string]int) {
	words := l.words()
	match := func(r Row, skip string) bool {
		if skip != "status" && l.get("status") != "" && py.OrS(r.Get("status"), "") != l.get("status") {
			return false
		}
		if skip != "source" && l.get("source") != "" && r.S("source") != l.get("source") {
			return false
		}
		inc := ""
		if r.T("incident_id") {
			inc = "#" + I2S(r.I("incident_id"))
		}
		return listMatch(words, r.Str("title"), rcaBase(r.S("path")), r.S("source"), inc)
	}
	stN, srcN := map[string]int{}, map[string]int{}
	var shown []Row
	for _, r := range items {
		if match(r, "status") {
			stN[py.OrS(r.Get("status"), "")]++
		}
		if match(r, "source") {
			srcN[r.S("source")]++
		}
		if match(r, "") {
			shown = append(shown, r)
		}
	}
	switch l.get("sort") {
	case "oldest":
		sort.SliceStable(shown, func(i, j int) bool { return py.Num(shown[i].Get("created")) < py.Num(shown[j].Get("created")) })
	case "cost":
		sort.SliceStable(shown, func(i, j int) bool { return py.Num(shown[i].Get("cost_usd")) > py.Num(shown[j].Get("cost_usd")) })
	}
	return shown, stN, srcN
}

// rcaDeepState is deep_state(iid): (can_start, message).
func rcaDeepState(c *Ctx, iid int64) (bool, string) {
	st := ReadJSONMap(filepath.Join(SET_OUT, "run-requests.json"))
	now := Now()
	if _, err := os.Stat(filepath.Join(SET_IN, "requests", "rca-deep-"+I2S(iid)+".req")); err == nil {
		return false, "Deep RCA requested; starting within seconds."
	}
	mine := py.AsMap(py.AsMap(st.Get("rca")).GetD(I2S(iid), py.NewOMap()))
	if mine == nil {
		mine = py.NewOMap()
	}
	started := py.Num(mine.GetD("started", int64(0)))
	who := ""
	if mine.Get("by") == "auto" {
		who = " (automatic)"
	}
	if started != 0 && now-started < 35*60 {
		latest := Q("SELECT created FROM rca_reports WHERE incident_id=? ORDER BY created DESC LIMIT 1", iid)
		if len(latest) == 0 || latest[0].F("created") < started {
			c.Refresh = true
			return false, "Deep RCA" + who + " running since " + Ago(mine.Get("started")) + " (up to 30 min)."
		}
	}
	if started != 0 && now-started < RcaGap {
		return false, "Last deep RCA" + who + " " + Ago(mine.Get("started")) + "; one per incident per hour."
	}
	if st.Get("rca_day") == time.Unix(int64(now), 0).In(time.Local).Format("2006-01-02") && py.Num(py.Or(st.Get("rca_count"), int64(0))) >= RcaPerDay {
		return false, "Daily limit of " + Itoa(RcaPerDay) + " deep RCAs reached."
	}
	res := py.Str(mine.GetD("result", ""))
	if strings.HasPrefix(res, "skipped") && now-py.Num(mine.GetD("requested", int64(0))) < 600 {
		return true, "Last request " + res
	}
	return true, ""
}

// RcaPostDeep is post_deep(form, sess): (status, location or message).
func RcaPostDeep(c *Ctx, form py.QS, sess *Session) (int, string) {
	iid := form.Get("id")
	if sess.Role != "admin" {
		return 403, "Only admins can start a deep RCA."
	}
	if !py.IsDigit(iid) || py.Len(iid) > 9 {
		return 400, "Invalid request."
	}
	n, _ := py.ParseInt(iid)
	if len(Q("SELECT id FROM incidents WHERE id=?", n)) == 0 {
		return 404, "No such incident."
	}
	if ok, msg := rcaDeepState(c, n); !ok {
		return 429, msg
	}
	d := filepath.Join(SET_IN, "requests")
	if err := os.MkdirAll(d, 0o777); err != nil {
		panic(err)
	}
	body := py.Dumps(py.NewOMap("by", sess.Username, "at", NowInt()), -1)
	if err := WriteFileAtomic(filepath.Join(d, "rca-deep-"+I2S(n)+".req"), filepath.Join(d, ".rca-deep-"+I2S(n)+".tmp"), []byte(body)); err != nil {
		panic(err)
	}
	logf("deep RCA of incident #%d requested by %s", n, sess.Username)
	return 303, "/incidents/view?id=" + I2S(n) + "&sent=deep"
}
