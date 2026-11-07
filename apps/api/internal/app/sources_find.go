package app

// sources_find.go: `argus sources find` routing as a JSON API (ADR-0017 P3), admins only, read-only.
//
//	GET /api/v1/sources/find?q=<service or question>&type=<t>[&type=<t>...]&release=1
//
// The same router as the agent's command (internal/sources/find) over ARGUS_DB source_routes, data_sources and
// settings-out/sources-status.json. The web process never fans out and never learns: it makes no outbound call and
// writes nothing; "fanout" is null and a type without a route says that the fan-out runs in the agent. No audit line
// either (the agent's command keeps the audit log); the question is never logged.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
	"github.com/sinaneysi/argus/internal/sources"
	"github.com/sinaneysi/argus/internal/sources/catalogue"
	"github.com/sinaneysi/argus/internal/sources/find"
)

func sourcesFindJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, &apiErr{403, "admins only"}
	}
	q := qs.Get("q")
	if strings.TrimSpace(q) == "" || len(q) > 500 || strings.IndexFunc(q, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return nil, &apiErr{400, "q is a service name or a question (1..500 printable characters)"}
	}
	var want []string
	for _, raw := range qs.List("type") {
		for _, x := range strings.Split(raw, ",") {
			if x = strings.TrimSpace(x); x == "" {
				continue
			}
			t, ok := find.Canon(x)
			if !ok {
				return nil, &apiErr{400, "type must be one of " + strings.Join(find.Order, ", ")}
			}
			if !In(t, want...) {
				want = append(want, t)
			}
		}
	}
	o := find.Options{Entries: sources.LoadStatus(filepath.Join(SET_OUT, "sources-status.json")),
		Limits: find.LimitsFrom(func(k string) string { return env(k, "") }), NoFanout: "fan-out runs in the agent"}
	var db catalogue.DB
	if _, err := os.Stat(DB); err == nil {
		if c, err := roDB(DB); err == nil {
			db = c
		}
	}
	o.DB = db
	svcs, _, err := catalogue.LoadServices(env("ARGUS_SOURCES_SERVICES", ""), db)
	if err != nil {
		logf("sources find: the service catalogue could not be read: %v", err)
		svcs = catalogue.NewServices(nil, nil)
	}
	o.Services = svcs
	return find.Find(q, want, In(qs.Get("release"), "1", "true", "yes"), false, o), nil
}
