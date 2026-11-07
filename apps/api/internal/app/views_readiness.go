package app

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

// views_readiness.py: Overview card "Sales-event readiness" (bin/readiness.py output), shown from D-before to
// D+after around the event date of the admin settings.

// ReadinessData is ARGUS_READINESS_FILE (old name OPS_READINESS_DATA).
var ReadinessData = env("ARGUS_READINESS_FILE", "/var/lib/argus/readiness.json")

var readinessTZ = SiteTZ // the schedules' zone (site.go)

// ReadinessSettings is settings(S).
func ReadinessSettings() *py.OMap {
	cfg := ReadJSON(settingsPath(), py.NewOMap())
	var raw any
	if m := py.AsMap(cfg); m != nil {
		raw = m.Get("readiness")
	}
	return merged(READINESS, raw)
}

type readinessWin struct {
	d          int64
	in, freeze bool
	freezeFrom string
}

func readinessParseDate(s string) (time.Time, bool) {
	for _, l := range []string{"2006-01-02", "20060102"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// readinessWindow is window(cfg, today); today is a date in the site time zone.
func readinessWindow(cfg *py.OMap, today time.Time) *readinessWin {
	ev, ok := readinessParseDate(py.Str(cfg.Get("event_date")))
	if !ok {
		return nil
	}
	td := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	d := int64(math.Round(ev.Sub(td).Hours() / 24))
	after, before, fz := py.I(cfg.Get("after_days")), py.I(cfg.Get("before_days")), py.I(cfg.Get("freeze_days"))
	return &readinessWin{d: d, in: -after <= d && d <= before, freeze: 0 <= d && d <= fz,
		freezeFrom: ev.AddDate(0, 0, -int(fz)).Format("2006-01-02")}
}

func readinessDLabel(d int64) string {
	if d > 0 {
		return "D-" + I2S(d)
	} else if d == 0 {
		return "D-day"
	}
	return "D+" + I2S(-d)
}

func readinessPct(v any) string {
	if v == nil {
		return "-"
	}
	return py.F(py.Num(v), 0) + " %"
}

// readinessComma is f"{n:,}".
func readinessComma(v any) string {
	if f, ok := v.(float64); ok {
		s := py.FloatRepr(f)
		ip, frac, _ := strings.Cut(s, ".")
		n, err := strconv.ParseInt(ip, 10, 64)
		if err != nil {
			return s
		}
		return py.Comma(n) + "." + frac
	}
	return py.Comma(py.I(v))
}

// ReadinessToday is the date the window is computed for (tests may pin it).
var ReadinessToday = func() time.Time { return time.Now().In(readinessTZ) }

// ReadinessSection is views_readiness.section(server).
// readinessDoc is the event readiness report of the Checks page.
type readinessDoc struct {
	title, date, when string
	rows              []readinessRow
	gaps              []string
}

type readinessRow struct{ k, v, st string }

// readinessData is the report while the event window is open, else nil.
func readinessData() *readinessDoc {
	cfg := ReadinessSettings()
	w := readinessWindow(cfg, ReadinessToday())
	if w == nil || !w.in {
		return nil
	}
	doc := py.AsMap(ReadJSON(ReadinessData, py.NewOMap()))
	if doc == nil {
		doc = py.NewOMap()
	}
	var rows []readinessRow
	days := py.AsList(py.Or(py.AsMap(py.Or(doc.Get("deploys"), nil)).Get("days"), []any{}))
	if len(days) > 0 {
		var week float64
		weekInt := true
		var per []string
		for i, dv := range days {
			d := py.AsMap(dv)
			rel := d.GetD("release", int64(0))
			if _, isF := rel.(float64); isF {
				weekInt = false
			}
			week += py.Num(rel)
			if i < 7 {
				per = append(per, py.Slice(py.Str(d.Get("day")), 5)+" "+py.Str(rel))
			}
		}
		ws := strconv.FormatInt(int64(week), 10)
		if !weekInt {
			ws = py.FloatRepr(week)
		}
		st := "OK"
		if w.freeze && py.Truthy(py.AsMap(days[0]).Get("release")) {
			st = "FAIL"
		}
		rows = append(rows, readinessRow{"Release deploys", ws + " in 7 days (" + strings.Join(per, " · ") + ")", st})
	}
	if w.freeze {
		rows = append(rows, readinessRow{"Deploy freeze", "since " + w.freezeFrom + ": no release deploy without sign-off", "WARN"})
	} else if w.d > 0 {
		rows = append(rows, readinessRow{"Deploy freeze", "from " + w.freezeFrom + " (D-" + I2S(py.I(cfg.Get("freeze_days"))) + ")", "OK"})
	}
	n := 0
	for _, xv := range py.AsList(py.Or(doc.Get("capacity"), []any{})) {
		x := py.AsMap(xv)
		if x.Get("worst") == nil {
			continue
		}
		if n >= 5 {
			break
		}
		n++
		worst := py.Num(x.Get("worst"))
		st := "OK"
		if worst >= 90 {
			st = "FAIL"
		} else if worst >= 75 {
			st = "WARN"
		}
		rows = append(rows, readinessRow{"Capacity " + py.Str(x.Get("host")), "7 d peak cpu " + readinessPct(x.Get("cpu_peak")) + " · mem " +
			readinessPct(x.Get("mem_peak")) + " · disk " + readinessPct(x.Get("disk_max")) + " " + py.OrS(x.Get("disk_mount"), ""), st})
	}
	e4 := py.AsMap(py.Or(doc.Get("edge429"), nil))
	if e4.Get("total") != nil {
		var tops []string
		for i, hv := range py.AsList(py.Or(e4.Get("top_hosts"), []any{})) {
			if i >= 3 {
				break
			}
			hn := py.AsList(hv)
			if len(hn) == 2 {
				tops = append(tops, py.Str(hn[0])+" "+py.Str(hn[1]))
			}
		}
		top := strings.Join(tops, ", ")
		v := readinessComma(e4.Get("total"))
		if py.Truthy(e4.Get("requests")) {
			v += " of " + readinessComma(e4.Get("requests")) + " requests"
		}
		if top != "" {
			v += " · top " + top
		}
		st := "OK"
		if py.Truthy(e4.Get("total")) && py.Truthy(e4.Get("requests")) && py.Num(e4.Get("total"))/py.Num(e4.Get("requests")) > 0.001 {
			st = "WARN"
		}
		rows = append(rows, readinessRow{"Edge 429 (24 h)", v, st})
	}
	for _, pv := range py.AsList(py.Or(doc.Get("pages"), []any{})) {
		p := py.AsMap(pv)
		u := py.Str(p.Get("url"))
		path := u
		if i := strings.Index(u, PUBLIC_DOMAIN); PUBLIC_DOMAIN != "" && i >= 0 {
			path = u[i+len(PUBLIC_DOMAIN):]
		}
		if path == "" {
			path = "/"
		}
		v := py.OrS(p.Get("status"), "no answer")
		if py.Truthy(p.Get("time")) {
			v += " in " + py.F(py.Num(p.Get("time")), 1) + " s"
		}
		is200 := py.IsNumber(p.Get("status")) && py.Num(p.Get("status")) == 200
		st := "FAIL"
		if is200 && py.Num(py.Or(p.Get("time"), int64(0))) < 5 {
			st = "OK"
		} else if is200 {
			st = "WARN"
		}
		rows = append(rows, readinessRow{"Page " + path, v, st})
	}
	rd := py.AsMap(py.Or(doc.Get("redis_admin"), nil))
	if rd.Get("used") != nil && py.Truthy(rd.Get("host_ram")) {
		share := py.Num(rd.Get("used")) / py.Num(rd.Get("host_ram")) * 100
		mm := "0 (no cap)"
		if py.Truthy(rd.Get("maxmemory")) {
			mm = py.Str(rd.Get("maxmemory"))
		}
		st := "OK"
		if share >= 70 {
			st = "FAIL"
		} else if share >= 40 {
			st = "WARN"
		}
		rows = append(rows, readinessRow{"redis-admin memory", py.F(py.Num(rd.Get("used"))/1e9, 1) + " GB of " + py.F(py.Num(rd.Get("host_ram"))/1e9, 0) +
			" GB host RAM (" + py.F(share, 1) + " %), maxmemory " + mm, st})
	}
	crit := Q("SELECT count(*) n FROM incidents WHERE status IN ('open','acknowledged') AND severity='critical'")
	var cn any = int64(0)
	if len(crit) > 0 {
		cn = crit[0].Get("n")
	}
	st := "OK"
	if py.Truthy(cn) {
		st = "FAIL"
	}
	rows = append(rows, readinessRow{"Critical incidents", py.Str(cn) + " open", st})
	when := "not collected yet (runs with the daily insights)"
	if py.Truthy(doc.Get("at")) {
		when = "collected " + Ago(doc.Get("at"))
	}
	var gaps []string
	if errs := py.AsList(doc.Get("errors")); py.Truthy(doc.Get("errors")) {
		for i, x := range errs {
			if i >= 3 {
				break
			}
			gaps = append(gaps, py.Str(x))
		}
	}
	return &readinessDoc{title: py.Str(cfg.Get("event_name")) + " readiness · " + readinessDLabel(w.d), date: py.Str(cfg.Get("event_date")), when: when, rows: rows, gaps: gaps}
}
