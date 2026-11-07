package app

// sources_catalogue.go: the Data sources catalogue as a JSON API (ADR-0017 P2; its UI belongs to the Next.js frontend,
// docs/ROADMAP.md). Reads ARGUS_DB only (source_routes, source_unmapped, source_discovery_runs,
// source_action_results). An action (confirm, reject, edit a route, Discover now) is a request file in
// settings-in/source-actions/<user>-<16 hex>.json that the agent's `argus sources actions` applies; the answer is 202
// with the request id, and GET .../action?rid= reports its result. Auth, CSRF and 2FA: api.go.
//
//	GET  /api/v1/sources/catalogue                  summary: verdict, counts, the last discovery run per source
//	GET  /api/v1/sources/catalogue/routes           ?service= ?q= ?type= ?status= ?page= ?per=
//	GET  /api/v1/sources/catalogue/unmapped         ?type= ?page= ?per=
//	GET  /api/v1/sources/catalogue/runs             ?source= ?page= ?per=   every recorded discovery run, newest first
//	GET  /api/v1/sources/catalogue/action           ?rid=   the state of one request of this user
//	POST /api/v1/sources/catalogue/routes/confirm   {"id"}            admins, recent 2FA, CSRF
//	POST /api/v1/sources/catalogue/routes/reject    {"id"}
//	POST /api/v1/sources/catalogue/routes/edit      {"id", "locator", "note"}
//	POST /api/v1/sources/catalogue/discover         {"source"}

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

const catAPI = "/api/v1/sources/catalogue"

var (
	catSourceRe  = regexp.MustCompile(`^[a-z]{3,20}/[a-z0-9][a-z0-9-]{0,30}$`)
	catServiceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,120}$`)
	catTypes     = []string{"gitlab", "kubernetes", "grafana", "sentry", "elasticsearch"}
	catStatuses  = []string{"proposed", "confirmed", "stale", "rejected"}
)

// CatRoute is one route as the API returns it.
type CatRoute struct {
	ID         int64   `json:"id"`
	Service    string  `json:"service"`
	Source     string  `json:"source"`
	Type       string  `json:"type"`
	Kind       string  `json:"kind"`
	Locator    string  `json:"locator"`
	Label      string  `json:"label"`
	Origin     string  `json:"origin"`
	Status     string  `json:"status"` // proposed | confirmed | stale | rejected (stale: confirmed, not seen by the last discovery)
	Rule       string  `json:"rule"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
	UpdatedBy  string  `json:"updated_by"`
	UpdatedAt  int64   `json:"updated_at"`
	Stale      bool    `json:"-"` // not seen by the last discovery, whatever the status
}

// CatRun is one discovery run.
type CatRun struct {
	Source     string         `json:"source"`
	Status     string         `json:"status"` // ok | partial | error | skipped
	Mode       string         `json:"mode"`
	Trigger    string         `json:"trigger"`
	Counts     map[string]int `json:"counts"`
	Calls      int64          `json:"calls"`
	Note       string         `json:"note"`
	StartedAt  int64          `json:"started_at"`
	FinishedAt int64          `json:"finished_at"`
}

func catType(source string) string { t, _, _ := strings.Cut(source, "/"); return t }

func catRoutes() []CatRoute {
	out := []CatRoute{}
	for _, r := range Q(`SELECT id, service, source, kind, locator, label, origin, status, rule, evidence, updated_by, confidence,
  stale, updated_at FROM source_routes ORDER BY service, status='confirmed' DESC, confidence DESC, source, kind, locator`) {
		st := r.S("status")
		if st == "confirmed" && r.I("stale") != 0 {
			st = "stale"
		}
		out = append(out, CatRoute{ID: r.I("id"), Service: r.S("service"), Source: r.S("source"), Type: catType(r.S("source")),
			Kind: r.S("kind"), Locator: r.S("locator"), Label: r.S("label"), Origin: r.S("origin"), Status: st, Rule: r.S("rule"),
			Reason: r.S("evidence"), UpdatedBy: r.S("updated_by"), Confidence: r.F("confidence"), UpdatedAt: r.I("updated_at"), Stale: r.I("stale") != 0})
	}
	return out
}

func catRunOf(r Row) CatRun {
	x := CatRun{Source: r.S("source"), Status: r.S("status"), Mode: r.S("mode"), Trigger: r.S("trigger"), Note: SafeErr(r.S("error")),
		Calls: r.I("calls"), StartedAt: r.I("started_at"), FinishedAt: r.I("finished_at"), Counts: map[string]int{}}
	cv, _ := py.ParseJSON([]byte(r.S("counts")))
	if m := py.AsMap(cv); m != nil {
		for _, k := range m.Keys() {
			x.Counts[k] = int(py.I(m.Get(k)))
		}
	}
	return x
}

// catLastRuns is the newest run per source that was not skipped.
func catLastRuns() []CatRun {
	out := []CatRun{}
	for _, r := range Q(`SELECT r.* FROM source_discovery_runs r WHERE r.id = (SELECT MAX(id) FROM source_discovery_runs x
  WHERE x.source = r.source AND x.status != 'skipped') ORDER BY r.source`) {
		out = append(out, catRunOf(r))
	}
	return out
}

func catOneOf(qs py.QS, key string, vals []string) (string, *apiErr) {
	v := qs.Get(key)
	if v == "" || In(v, vals...) {
		return v, nil
	}
	return "", &apiErr{400, key + " must be one of " + strings.Join(vals, ", ")}
}

// --- GET ------------------------------------------------------------------------------------------------------------

func catSummaryJSON(py.QS, *Session) (any, *apiErr) {
	routes := catRoutes()
	runs := catLastRuns()
	counts := map[string]int{"confirmed": 0, "proposed": 0, "stale": 0, "rejected": 0}
	svcs := map[string]bool{}
	for _, r := range routes {
		counts[r.Status]++
		if r.Status != "rejected" {
			svcs[r.Service] = true
		}
	}
	unmapped := 0
	if r := Q("SELECT COUNT(*) AS n FROM source_unmapped"); len(r) > 0 {
		unmapped = int(r[0].I("n"))
	}
	failed := []string{}
	var last int64
	for _, r := range runs {
		if r.Status == "error" {
			failed = append(failed, r.Source)
		}
		last = max(last, r.FinishedAt)
	}
	kind, head := "ok", "Every route is confirmed or rejected."
	switch {
	case len(runs) == 0 && len(routes) == 0:
		kind, head = "none", "No catalogue yet."
	case len(failed) > 0:
		kind, head = "warn", strings.Join(failed, ", ")+" could not be read by the last discovery."
	case counts["proposed"] > 0:
		kind, head = "review", I2S(int64(counts["proposed"]))+If(counts["proposed"] == 1, " route waits", " routes wait")+" for a person."
	}
	return map[string]any{"verdict": map[string]any{"kind": kind, "head": head, "failed_sources": failed},
		"counts": map[string]any{"services": len(svcs), "confirmed": counts["confirmed"], "proposed": counts["proposed"],
			"stale": counts["stale"], "rejected": counts["rejected"], "unmapped": unmapped, "routes": len(routes)},
		"last_discovery": last, "sources": runs}, nil
}

func catRoutesJSON(qs py.QS, _ *Session) (any, *apiErr) {
	typ, e := catOneOf(qs, "type", catTypes)
	if e != nil {
		return nil, e
	}
	status, e := catOneOf(qs, "status", catStatuses)
	if e != nil {
		return nil, e
	}
	svc := qs.Get("service")
	if svc != "" && !catServiceRe.MatchString(svc) {
		return nil, &apiErr{400, "service is a name like shop/cart"}
	}
	q := qs.Get("q")
	if len([]rune(q)) > 100 {
		q = string([]rune(q)[:100])
	}
	words := strings.Fields(strings.ToLower(q))
	match := func(r CatRoute, skip string) bool {
		return (svc == "" || r.Service == svc) && (skip == "type" || typ == "" || r.Type == typ) &&
			(skip == "status" || status == "" || r.Status == status) && listMatch(words, r.Service)
	}
	typeN, statN := map[string]int{}, map[string]int{}
	out := []CatRoute{}
	for _, r := range catRoutes() {
		if match(r, "type") {
			typeN[r.Type]++
		}
		if match(r, "status") {
			statN[r.Status]++
		}
		if match(r, "") {
			out = append(out, r)
		}
	}
	pg, from, to := apiPaging(qs, len(out))
	return map[string]any{"page": pg, "routes": out[from:to], "facets": map[string]any{"type": typeN, "status": statN}}, nil
}

type catUnmapped struct {
	Source string `json:"source"`
	Type   string `json:"type"`
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Label  string `json:"label"`
	Note   string `json:"note"`
}

func catUnmappedJSON(qs py.QS, _ *Session) (any, *apiErr) {
	typ, e := catOneOf(qs, "type", catTypes)
	if e != nil {
		return nil, e
	}
	out := []catUnmapped{}
	for _, r := range Q("SELECT source, kind, key, label, note FROM source_unmapped ORDER BY source, kind, key") {
		if typ == "" || catType(r.S("source")) == typ {
			out = append(out, catUnmapped{r.S("source"), catType(r.S("source")), r.S("kind"), r.S("key"), r.S("label"), r.S("note")})
		}
	}
	pg, from, to := apiPaging(qs, len(out))
	return map[string]any{"page": pg, "unmapped": out[from:to]}, nil
}

func catRunsJSON(qs py.QS, _ *Session) (any, *apiErr) {
	src := qs.Get("source")
	if src != "" && !catSourceRe.MatchString(src) {
		return nil, &apiErr{400, "source is type/name, e.g. gitlab/main"}
	}
	out := []CatRun{}
	for _, r := range Q("SELECT * FROM source_discovery_runs ORDER BY id DESC") {
		if src == "" || r.S("source") == src {
			out = append(out, catRunOf(r))
		}
	}
	pg, from, to := apiPaging(qs, len(out))
	return map[string]any{"page": pg, "runs": out[from:to]}, nil
}

func catActionJSON(qs py.QS, sess *Session) (any, *apiErr) {
	rid := qs.Get("rid")
	if !jiraRidRe.MatchString(rid) {
		return nil, &apiErr{400, "rid is the 16-hex request id"}
	}
	if boardExists(filepath.Join(SET_IN, "source-actions", sess.Username+"-"+rid+".json")) {
		return map[string]any{"rid": rid, "state": "queued", "result": ""}, nil
	}
	rows := Q("SELECT result, action, at FROM source_action_results WHERE rid=? AND user=?", rid, sess.Username)
	if len(rows) == 0 {
		return map[string]any{"rid": rid, "state": "working", "result": ""}, nil
	}
	res, state := rows[0].S("result"), "done"
	if strings.HasPrefix(res, "refused: ") {
		state, res = "refused", strings.TrimPrefix(res, "refused: ")
	}
	return map[string]any{"rid": rid, "state": state, "action": rows[0].S("action"), "result": SafeErr(res), "at": rows[0].I("at")}, nil
}

// --- POST -----------------------------------------------------------------------------------------------------------

// catPost validates one action and writes its request file: 202 {"rid", "state": "queued"}.
func catPost(path string, body map[string]any, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, &apiErr{403, "admins only"}
	}
	if path == srcRevAct {
		var e *apiErr
		if path, e = srcReviewPath(body); e != nil {
			return nil, e
		}
	}
	p := py.NewOMap()
	switch path {
	case catAPI + "/discover":
		src, _ := body["source"].(string)
		if !catSourceRe.MatchString(src) {
			return nil, &apiErr{400, "source is type/name, e.g. gitlab/main"}
		}
		p.Set("action", "discover")
		p.Set("source", src)
	default:
		action := path[strings.LastIndex(path, "/")+1:]
		var id int64
		switch v := body["id"].(type) {
		case float64:
			if v == float64(int64(v)) {
				id = int64(v)
			}
		case string:
			id, _ = strconv.ParseInt(v, 10, 64)
		}
		if id < 1 || id > 1e9 {
			return nil, &apiErr{400, "id is a route id"}
		}
		p.Set("action", action)
		p.Set("id", id)
		if action == "edit" {
			loc, _ := body["locator"].(string)
			note, _ := body["note"].(string)
			loc, note = strings.Join(strings.Fields(loc), " "), strings.Join(strings.Fields(note), " ")
			if n := len([]rune(loc)); n < 1 || n > 250 || strings.ContainsAny(loc, "`|") {
				return nil, &apiErr{400, "locator must be 1 to 250 characters, without ` or |"}
			}
			if len([]rune(note)) > 300 || strings.Contains(note, "|") {
				return nil, &apiErr{400, "note is up to 300 characters, without |"}
			}
			p.Set("locator", loc)
			p.Set("note", note)
		}
		if action == "reject" {
			reason, _ := body["reason"].(string)
			reason = strings.Join(strings.Fields(reason), " ")
			if len([]rune(reason)) > 300 || strings.Contains(reason, "|") {
				return nil, &apiErr{400, "reason is up to 300 characters, without |"}
			}
			if reason != "" {
				p.Set("reason", reason)
			}
		}
	}
	rid := auth.TokenHex(8)
	p.Set("user", sess.Username)
	p.Set("role", sess.Role)
	p.Set("at", NowInt())
	if err := boardDropExcl(filepath.Join(SET_IN, "source-actions"), sess.Username+"-"+rid+".json", []byte(py.Dumps(p, -1))); err != nil {
		logf("save failed: %v", err)
		return nil, &apiErr{500, "could not save the request"}
	}
	logf("data source catalogue %s requested by %s: %s", py.Str(p.Get("action")), sess.Username, rid)
	return map[string]any{"rid": rid, "state": "queued", "status_url": catAPI + "/action?rid=" + rid}, nil
}
