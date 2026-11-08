package app

// needs.go: the data-source gate of pages and API routes (ADR-0021 D5, internal/needs). A gated API GET whose
// sources are all missing answers 200 {"notConfigured": {"sources": [...]}} instead of its data; GET /api/v1/needs
// tells the frontend the state of every kind and, with ?page=, whether that page is not configured.

import (
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/needs"
	"github.com/sinaneysi/argus/internal/py"
)

// needsDetector builds the detector from the configuration (tests replace it).
var needsDetector = func() needs.Detector {
	d := needs.FromConfig(config.Lookup, DEMO)
	d.StatusFile = SET_OUT + "/sources-status.json" // tests and the legacy layout move SET_OUT
	return d
}

// needsNow is the configured state of every kind, read now (a save applies at once).
func needsNow() needs.State { return needsDetector().Now() }

// apiNotConfigured is the answer of a gated API route whose sources are missing (nil: serve the route).
func apiNotConfigured(path string) map[string]any {
	n, ok := needs.Match(needs.APIs, path)
	if !ok {
		return nil
	}
	if miss := needsNow().Missing(n.AnyOf); len(miss) > 0 {
		return needs.Answer(miss)
	}
	return nil
}

// apiNeedsJSON is GET /api/v1/needs[?page=/cicd]: every kind with configured true/false, and notConfigured for the page.
func apiNeedsJSON(qs py.QS, sess *Session) (any, *apiErr) {
	st := needsNow()
	kinds := []map[string]any{}
	for _, k := range needs.Kinds {
		kinds = append(kinds, map[string]any{"key": k.Key, "label": k.Label, "settings": k.Settings, "configured": st[k.Key]})
	}
	out := map[string]any{"kinds": kinds}
	if n, ok := needs.Match(needs.Pages, qs.Get("page")); ok {
		if miss := st.Missing(n.AnyOf); len(miss) > 0 {
			out["notConfigured"] = needs.Answer(miss)["notConfigured"]
		}
	}
	return out, nil
}
