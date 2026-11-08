package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/needs"
)

// In the volume layout a gated API route whose source is missing answers 200 {"notConfigured"}; /api/v1/needs says
// which page is not configured; a Data sources entry of the type turns the route on (ADR-0021 D5).
func TestNeedsGate(t *testing.T) {
	e, ck, _ := srcSetup(t)
	old := needsDetector
	t.Cleanup(func() { needsDetector = old })
	needsDetector = func() needs.Detector {
		return needs.Detector{Active: true, Lookup: func(string) config.Value { return config.Value{Source: "default", Set: true} },
			StatusFile: filepath.Join(SET_OUT, "sources-status.json")}
	}
	code, m := apiReq(t, e, "GET", "/api/v1/cicd", ck, "", nil)
	nc, _ := m["notConfigured"].(map[string]any)
	if code != 200 || nc == nil || nc["sources"].([]any)[0].(map[string]any)["key"] != "gitlab" {
		t.Fatalf("cicd without GitLab: %d %v", code, m)
	}
	if code, m := apiReq(t, e, "GET", "/api/v1/cicd", nil, "", nil); code != 401 {
		t.Fatalf("anonymous still 401: %d %v", code, m)
	}
	if _, m := apiReq(t, e, "GET", "/api/v1/needs?page=/cicd/audit", ck, "", nil); m["notConfigured"] == nil || len(m["kinds"].([]any)) != len(needs.Kinds) {
		t.Fatalf("needs page: %v", m)
	}
	if _, m := apiReq(t, e, "GET", "/api/v1/needs?page=/today", ck, "", nil); m["notConfigured"] != nil {
		t.Fatalf("today needs nothing: %v", m)
	}
	os.MkdirAll(SET_OUT, 0o755)
	os.WriteFile(filepath.Join(SET_OUT, "sources-status.json"), []byte(`{"sources":{"gitlab/main":{"status":"readonly"}}}`), 0o644)
	if code, m := apiReq(t, e, "GET", "/api/v1/cicd", ck, "", nil); code != 200 || m["notConfigured"] != nil {
		t.Fatalf("cicd with GitLab: %d %v", code, m)
	}
	// legacy layout (the default): never gated
	needsDetector = old
	os.Remove(filepath.Join(SET_OUT, "sources-status.json"))
	if _, m := apiReq(t, e, "GET", "/api/v1/qa", ck, "", nil); m["notConfigured"] != nil {
		t.Fatalf("legacy gated: %v", m)
	}
}
