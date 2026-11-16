package taskagent

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updatePrompts = flag.Bool("update-prompts", false, "rewrite testdata/prompts/*.golden from prompts/")

// TestPromptSnapshots: a prompt change is a reviewed change. The embedded prompts must equal the snapshots in
// testdata/prompts; after a deliberate edit run `go test ./internal/taskagent -run Snapshots -update-prompts`.
func TestPromptSnapshots(t *testing.T) {
	for name, text := range map[string]string{"task-intake": intakePrompt, "task-run": runPrompt, "task-chat": chatPrompt} {
		p := filepath.Join("testdata", "prompts", name+".golden")
		if *updatePrompts {
			if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != text {
			t.Errorf("%s changed: review it, then update the snapshot with -update-prompts", name)
		}
	}
}

// TestPromptRules: the capability rules of ADR-0022 D9 are in the prompts.
func TestPromptRules(t *testing.T) {
	for _, must := range []string{
		"`capabilities`",                               // the catalogue in the task file
		`"input:<NAME>"`,                               // value_ref instead of literals
		"A secret value is never written",              // never a literal
		"`environment_scope` is exactly `\"develop\"`", // never * scope
		"Never `\"*\"`",
		`"mode": "operator"`, // pipelines and jobs are operator steps
		"Never start a pipeline",
		"`http.check`", "`gitlab.ci-variable.get`", // verify steps
		"read-only|WRITE|operator",
		"WRITE\n  steps stay a proposal", // outside develop
	} {
		if !strings.Contains(intakePrompt, must) {
			t.Errorf("task-intake.md lacks %q", must)
		}
	}
	for _, must := range []string{
		"**You change nothing.**", "`capability_results`", "Report them as they are",
		"Never repeat\n  a step, never guess a value, never claim a change that is not in `capability_results`",
		"PRODUCTION CHANGE", "You never start a pipeline or a job",
	} {
		if !strings.Contains(runPrompt, must) {
			t.Errorf("task-run.md lacks %q", must)
		}
	}
	if strings.Contains(runPrompt, "This run changes nothing") {
		t.Error("task-run.md still says the old \"This run changes nothing\"")
	}
	if strings.Contains(chatPrompt, "capability") {
		t.Error("task-chat.md must stay unchanged: chat never executes")
	}
}
