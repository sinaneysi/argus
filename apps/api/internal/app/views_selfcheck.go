package app

import (
	"math"

	"strconv"

	"github.com/sinaneysi/argus/internal/py"
)

// views_selfcheck.py: Overview section "Argus health" from bin/selfcheck.py's result.

// SelfcheckPath is OPS_SELFCHECK.
var SelfcheckPath = env("ARGUS_SELFCHECK_FILE", "/var/lib/argus/selfcheck.json")

const selfcheckStale = 15 * 60

// SelfcheckSummary is summary(doc, now): (status, line).
func SelfcheckSummary(doc *py.OMap, now float64) (any, string) {
	if now == 0 {
		now = Now()
	}
	if doc.Len() == 0 || !py.Truthy(doc.Get("ts")) {
		return "WARN", "No self-check yet (argus-selfcheck.timer not running?)"
	}
	ts := py.Num(doc.Get("ts"))
	if now-ts > selfcheckStale {
		return "WARN", "Self-check is stale: last result " + Ago(ts)
	}
	checks := py.AsList(py.Or(doc.Get("checks"), []any{}))
	bad := 0
	for _, cv := range checks {
		if py.AsMap(cv).Get("status") != "OK" {
			bad++
		}
	}
	line := Itoa(len(checks)-bad) + " of " + Itoa(len(checks)) + " parts OK"
	if fs := doc.Get("failing_since"); py.Truthy(fs) {
		line += " · failing for " + strconv.FormatInt(int64(math.Floor((now-py.Num(fs))/60)), 10) + " min"
	}
	return doc.GetD("status", "WARN"), line
}
