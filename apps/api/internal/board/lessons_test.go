package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The task agent's output proposes lessons; the loaded ones are counted; an admin approves one through a request;
// the knowledge file shows it.
func TestAgentLessons(t *testing.T) {
	e := agentEnv(t)
	e.o.KnowledgeDir = filepath.Join(e.d, "knowledge")
	e.run(t)
	p := plan(map[string]any{"text": "List the largest folders", "mode": "read-only"})
	p["lessons"] = []any{
		map[string]any{"title": "Build cache fills /var on build hosts", "body": "The cache folder grows 5 GB a day; prune it weekly.",
			"service": "group-a/build-runner", "env": "develop", "kind": "investigate", "tags": []any{"disk", "cache"}},
		map[string]any{"title": "", "body": "no title"},
	}
	e.out(t, "OPS-7", "intake", p, map[string]any{"cost_usd": 0.2, "is_error": false, "run_id": "12"})
	e.run(t)
	if got := e.q(t, `SELECT state || ':' || review || ':' || source_key || ':' || source_run FROM lessons`); got != "proposed:new:OPS-7:12" {
		t.Fatal(got)
	}
	if got := e.q(t, `SELECT text FROM agent_log WHERE key='OPS-7' AND kind='lessons'`); !strings.HasPrefix(got,
		"Argus learned 1 thing: review on /admin/lessons. dropped: ") {
		t.Fatal(got)
	}
	viewer := e.req(t, map[string]any{"kind": "lesson", "action": "approve", "lesson_id": 1, "by": "sam.viewer"})
	ok := e.req(t, map[string]any{"kind": "lesson", "action": "approve", "lesson_id": 1})
	st := e.run(t)
	if result(st, viewer) != "refused: only admins review lessons" || result(st, ok) != "lesson approved" {
		t.Fatal(result(st, viewer), result(st, ok))
	}
	if e.q(t, `SELECT state FROM lessons WHERE id=1`) != "approved" || e.q(t, `SELECT count(*) FROM events WHERE kind='lessons'`) != "1" {
		t.Fatal("not approved")
	}
	b, err := os.ReadFile(filepath.Join(e.d, "knowledge", "services", "group-a", "build-runner.md"))
	if err != nil || !strings.Contains(string(b), "Build cache fills /var") {
		t.Fatalf("knowledge file %q %v", b, err)
	}
	// the switch
	off := e.req(t, map[string]any{"kind": "lesson-settings", "enabled": false})
	if st := e.run(t); result(st, off) != "saved" || e.q(t, `SELECT v FROM state WHERE k='lessons_settings'`) != `{"enabled": false}` {
		t.Fatal(result(st, off))
	}
}
