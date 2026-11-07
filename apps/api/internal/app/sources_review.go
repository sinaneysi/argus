package app

// sources_review.go: the route decisions and the routing metrics as JSON (ADR-0017 P4).
//
//	GET  /api/v1/sources/review?why=&type=&q=   what waits for a person: learned routes, then discovery's strong proposals
//	GET  /api/v1/sources/review/item?id=        one route with its evidence
//	POST /api/v1/sources/review/act             {"action": "confirm|reject|edit", "id", "reason", "locator", "note"}
//	GET  /api/v1/sources/metrics                routes by type and status, the last days of finds, the top unmapped items
//
// Anyone signed in reads; acting is catPost's flow (admins, recent 2FA, CSRF, one request file; the demo refuses it).
// The full list stays in the catalogue: at most srcRevCap strong proposals wait here at a time. Argus has no Outline
// table yet, so there is no conflict reason and no tier order (ADR-0017 P4).

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

const (
	srcRevAPI      = "/api/v1/sources/review"
	srcRevAct      = srcRevAPI + "/act"
	srcMetricsAPI  = "/api/v1/sources/metrics"
	srcRevMinConf  = 0.9     // discovered proposals at or above this confidence join the queue
	srcRevCap      = 25      // at most this many at a time
	srcRevLearned  = 200     // learned routes read per request
	srcTopUnmapped = 5       // unmapped items the metrics name
	srcFindDays    = 7       // the period of the find counts
	srcFindMaxRead = 4 << 20 // bytes read from the end of the find log
)

// srcFindLog is ARGUS_SOURCES_FIND_AUDIT_LOG: one JSON line per find, no question text.
var (
	srcFindLog = env("ARGUS_SOURCES_FIND_AUDIT_LOG", CapacityData+"/sources-find.log")
	srcRunRe   = regexp.MustCompile(`^run ([A-Za-z0-9._:-]{1,80}):`)
)

func init() {
	apiGETs[srcRevAPI] = srcReviewJSON
	apiGETs[srcRevAPI+"/item"] = srcReviewItemJSON
	apiGETs[srcMetricsAPI] = srcMetricsJSON
}

// SrcRevItem is one route waiting for a decision.
type SrcRevItem struct {
	CatRoute
	Why   string `json:"why"`              // learned | discovered
	RunID string `json:"run_id,omitempty"` // the agent run that found a learned route
}

func srcRevItemOf(r CatRoute) SrcRevItem {
	it := SrcRevItem{CatRoute: r, Why: r.Origin}
	if m := srcRunRe.FindStringSubmatch(r.Reason); m != nil {
		it.RunID = m[1]
	}
	return it
}

// srcReviewItems is the queue in order (learned, then strong proposals; confidence, service, id inside each) and how
// many strong proposals the cap left in the catalogue.
func srcReviewItems() ([]SrcRevItem, int) {
	var learned, strong []SrcRevItem
	for _, r := range catRoutes() {
		switch {
		case r.Status != "proposed":
		case r.Origin == "learned":
			learned = append(learned, srcRevItemOf(r))
		case r.Origin == "discovered" && r.Confidence >= srcRevMinConf && !r.Stale:
			strong = append(strong, srcRevItemOf(r))
		}
	}
	order := func(x []SrcRevItem) {
		sort.SliceStable(x, func(i, j int) bool {
			a, b := x[i], x[j]
			if a.Confidence != b.Confidence {
				return a.Confidence > b.Confidence
			}
			if a.Service != b.Service {
				return a.Service < b.Service
			}
			return a.ID < b.ID
		})
	}
	order(learned)
	order(strong)
	if len(learned) > srcRevLearned {
		learned = learned[:srcRevLearned]
	}
	capped := 0
	if len(strong) > srcRevCap {
		capped = len(strong) - srcRevCap
		strong = strong[:srcRevCap]
	}
	return append(learned, strong...), capped
}

func srcReviewJSON(qs py.QS, _ *Session) (any, *apiErr) {
	why, e := catOneOf(qs, "why", []string{"learned", "discovered"})
	if e != nil {
		return nil, e
	}
	typ, e := catOneOf(qs, "type", catTypes)
	if e != nil {
		return nil, e
	}
	words := strings.Fields(strings.ToLower(qs.Get("q")))
	all, capped := srcReviewItems()
	items := []SrcRevItem{}
	counts := map[string]int{"learned": 0, "discovered": 0}
	for _, it := range all {
		counts[it.Why]++
		hay := strings.ToLower(strings.Join([]string{it.Service, it.Locator, it.Label, it.Reason, it.Source}, " "))
		ok := (why == "" || it.Why == why) && (typ == "" || it.Type == typ)
		for _, w := range words {
			ok = ok && strings.Contains(hay, w)
		}
		if ok {
			items = append(items, it)
		}
	}
	return map[string]any{"items": items, "total": len(all), "counts": counts, "capped": capped,
		"min_confidence": srcRevMinConf, "cap": srcRevCap}, nil
}

func srcReviewItemJSON(qs py.QS, _ *Session) (any, *apiErr) {
	id, err := strconv.ParseInt(qs.Get("id"), 10, 64)
	if err != nil || id < 1 || id > 1e9 {
		return nil, &apiErr{400, "id is a route id"}
	}
	all, _ := srcReviewItems()
	for _, it := range all {
		if it.ID != id {
			continue
		}
		out := map[string]any{"item": it, "found_by": "an agent run's fan-out"}
		if it.Why == "discovered" {
			out["found_by"] = "discovery"
			for _, r := range catLastRuns() {
				if r.Source == it.Source {
					out["discovery_run"] = r
				}
			}
		}
		return out, nil
	}
	return nil, &apiErr{404, "that decision is no longer waiting"}
}

// srcReviewPath maps an act body to the catalogue route catPost already serves.
func srcReviewPath(body map[string]any) (string, *apiErr) {
	action, _ := body["action"].(string)
	if !In(action, "confirm", "reject", "edit") {
		return "", &apiErr{400, "action must be one of confirm, reject, edit"}
	}
	return catAPI + "/routes/" + action, nil
}

// --- metrics --------------------------------------------------------------------------------------------------------

// srcFinds counts the last srcFindDays of the find log. Each find counts once, by the best answer it got. "log" says
// whether the numbers can be trusted: ok, missing (nothing asked yet) or unreadable (never shown as zero finds).
func srcFinds(path string, now int64) map[string]any {
	c := map[string]any{"days": srcFindDays, "log": "ok"}
	n := map[string]int64{"finds": 0, "confirmed": 0, "proposed": 0, "fanout": 0, "none": 0, "fanout_searches": 0, "fanout_hits": 0, "learned": 0}
	svcs := map[string]bool{}
	for _, ln := range srcLogTail(path, c) {
		var rec struct {
			At      int64                           `json:"at"`
			Service []string                        `json:"service"`
			Learned int64                           `json:"learned"`
			Chosen  []string                        `json:"chosen"`
			Fanout  map[string]struct{ Hits int64 } `json:"fanout"`
		}
		if json.Unmarshal([]byte(ln), &rec) != nil || rec.At < now-srcFindDays*86400 {
			continue
		}
		n["finds"]++
		n[srcFindOutcome(rec.Chosen, len(rec.Fanout) > 0)]++
		for _, f := range rec.Fanout {
			n["fanout_searches"]++
			n["fanout_hits"] += f.Hits
		}
		n["learned"] += rec.Learned
		for _, s := range rec.Service {
			svcs[s] = true
		}
	}
	for k, v := range n {
		c[k] = v
	}
	c["services"] = len(svcs)
	return c
}

func srcFindOutcome(chosen []string, fanout bool) string {
	best := "none"
	if fanout {
		best = "fanout"
	}
	for _, x := range chosen {
		if strings.HasSuffix(x, ":confirmed") {
			return "confirmed"
		}
		if strings.HasSuffix(x, ":proposed") {
			best = "proposed"
		}
	}
	return best
}

// srcLogTail is the last srcFindMaxRead bytes of the log, line by line; a line the cut split is dropped.
func srcLogTail(path string, c map[string]any) []string {
	f, err := os.Open(path)
	if err != nil {
		c["log"] = "unreadable"
		if errors.Is(err, fs.ErrNotExist) {
			c["log"] = "missing"
		}
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		c["log"] = "unreadable"
		return nil
	}
	cut := st.Size() > srcFindMaxRead
	if cut {
		if _, err = f.Seek(-srcFindMaxRead, io.SeekEnd); err != nil {
			c["log"] = "unreadable"
			return nil
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		c["log"] = "unreadable"
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if cut && len(lines) > 0 {
		lines = lines[1:]
	}
	return lines
}

func srcMetricsJSON(py.QS, *Session) (any, *apiErr) {
	per := map[string]map[string]int{}
	var types []string
	for _, r := range catRoutes() {
		if per[r.Type] == nil {
			per[r.Type] = map[string]int{"confirmed": 0, "proposed": 0, "rejected": 0, "stale": 0}
			types = append(types, r.Type)
		}
		per[r.Type][r.Status]++
	}
	sort.Strings(types)
	routes := []map[string]any{}
	for _, t := range types {
		c := per[t]
		routes = append(routes, map[string]any{"type": t, "confirmed": c["confirmed"], "proposed": c["proposed"], "rejected": c["rejected"], "stale": c["stale"]})
	}
	top := []map[string]any{}
	for _, r := range Q("SELECT source, kind, key, label, note FROM source_unmapped ORDER BY coalesce(note,'')='', source, kind, key LIMIT ?", srcTopUnmapped) {
		top = append(top, map[string]any{"source": r.S("source"), "kind": r.S("kind"), "key": r.S("key"), "label": r.S("label"), "note": r.S("note")})
	}
	total := int64(0)
	if r := Q("SELECT COUNT(*) AS n FROM source_unmapped"); len(r) > 0 {
		total = r[0].I("n")
	}
	return map[string]any{"routes": routes, "finds": srcFinds(srcFindLog, NowInt()), "unmapped": map[string]any{"total": total, "top": top}}, nil
}
