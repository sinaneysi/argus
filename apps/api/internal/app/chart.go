package app

import (
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

// Pt is one (ts, value) of a chart; Nil marks a None value (dropped, like the Python filter).
type Pt struct {
	T   float64
	V   float64
	Nil bool
}

func f1(x float64) string { return py.F(x, 1) }

// STRIP_H is the hours the run strip shows.
const STRIP_H = 48

type stripMark struct {
	X     string `json:"x"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// runStrip is the run strip of a task in a 480 x 16 box: the midnights, a mark per run, and its words.
type runStrip struct {
	Mids  []string    `json:"mids"`
	Marks []stripMark `json:"marks"`
	Label string      `json:"label"`
	Hours int         `json:"hours"`
}

func runStripOf(task string, now float64) runStrip {
	t0, w := now-STRIP_H*3600, 480.0
	runs := Q("SELECT started_at, status FROM runs WHERE task=? AND started_at>=? ORDER BY started_at", task, int64(t0))
	x := func(t float64) float64 { return (t - t0) / (now - t0) * w }
	day := LocalTime(t0)
	mid := float64(time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, time.Local).Unix())
	out := runStrip{Mids: []string{}, Marks: []stripMark{}, Hours: STRIP_H}
	for mid < now {
		out.Mids = append(out.Mids, f1(x(mid)))
		d := LocalTime(mid + 26*3600)
		mid = float64(time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local).Unix())
	}
	nBad := 0
	for _, r := range runs {
		st := strings.ToUpper(r.S("status"))
		if st == "" {
			st = "?"
		}
		kind := "fail"
		if strings.HasPrefix(st, "OK") {
			kind = "ok"
		} else if strings.HasPrefix(st, "WARN") {
			kind = "warn"
		}
		out.Marks = append(out.Marks, stripMark{f1(x(r.F("started_at"))), kind, Text(When(r.Get("started_at"))) + " · " + py.OrS(r.Get("status"), "?")})
		if !strings.HasPrefix(strings.ToUpper(r.S("status")), "OK") {
			nBad++
		}
	}
	out.Label = strconv.Itoa(len(runs)) + " runs in " + strconv.Itoa(STRIP_H) + " h, " + strconv.Itoa(nBad) + " not OK"
	return out
}
