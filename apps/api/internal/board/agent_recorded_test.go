package board

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/taskagent"
)

// recordedRegistry is the catalogue a recorded intake planned against: http.check on health.example.com and the
// GitLab variables of group/app (Validate only, no network).
func recordedRegistry(t *testing.T) *capability.Registry {
	reg, err := capability.Default(capability.Options{HTTPHosts: []string{"health.example.com"},
		GitLab: capability.GitLabOptions{URL: "https://git.example.com", Projects: []string{"group/app"}}})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// recorded is a recorded model answer (testdata/intake), read the way the runner reads it, then validated.
func recorded(t *testing.T, name string) (map[string]any, []any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "intake", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	d := taskagent.ExtractJSON(string(b))
	if d == nil {
		t.Fatalf("%s: no JSON in the recorded answer", name)
	}
	p, err := validatePlanWith(d, recordedRegistry(t))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return p, asList(p["plan"])
}

func TestRecordedIntakeGood(t *testing.T) {
	p, steps := recorded(t, "good")
	want := []struct{ mode, capability string }{{"read-only", "gitlab.ci-variable.get"}, {"WRITE", "gitlab.ci-variable.set"},
		{"operator", ""}, {"read-only", "http.check"}}
	for i, w := range want {
		s := asMap(steps[i])
		if s["mode"] != w.mode || orStr(s["capability"]) != w.capability || s["capability_error"] != nil {
			t.Fatalf("step %d: %v", i+1, s)
		}
	}
	if got, _ := spinUpMode(p, "on"); got != "execute" {
		t.Fatalf("path %s, want execute", got)
	}
	_, dp, err := capability.DecodePlan(mustJSON(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if in := capability.PlanInputs(dp); len(in[2]) != 1 || in[2][0] != "NEW_PAYMENT_API_KEY" {
		t.Fatalf("inputs %v", in)
	}
}

func TestRecordedIntakeLiteralSecretRefused(t *testing.T) {
	p, steps := recorded(t, "literal-secret")
	for i, sv := range steps {
		s := asMap(sv)
		if s["capability"] != "" || s["args"] != nil || orStr(s["capability_error"]) == "" {
			t.Fatalf("step %d kept a literal value: %v", i+1, s)
		}
	}
	if !strings.Contains(orStr(asMap(steps[0])["capability_error"]), "never a literal") {
		t.Fatalf("reason: %v", asMap(steps[0])["capability_error"])
	}
	if b := string(mustJSON(t, p)); strings.Contains(b, "Recorded-Literal") {
		t.Fatal("a literal value reached the stored plan")
	}
	if got, _ := spinUpMode(p, "on"); got != "write-report" {
		t.Fatalf("path %s, want write-report", got)
	}
}

func TestRecordedIntakePipelineWriteIsWriteReport(t *testing.T) {
	p, steps := recorded(t, "pipeline-write")
	if asMap(steps[0])["capability"] != "http.check" {
		t.Fatalf("the read step lost its capability: %v", steps[0])
	}
	for _, i := range []int{1, 2} {
		s := asMap(steps[i])
		if s["mode"] != "WRITE" || s["capability"] != "" || orStr(s["capability_error"]) == "" {
			t.Fatalf("step %d: %v", i+1, s)
		}
	}
	if got, _ := spinUpMode(p, "on"); got != "write-report" {
		t.Fatalf("path %s, want write-report", got)
	}
}

func TestRecordedIntakeInjectionStarScopeRefused(t *testing.T) {
	p, steps := recorded(t, "injection-star")
	star, other := asMap(steps[0]), asMap(steps[1])
	if star["capability"] != "" || !strings.Contains(orStr(star["capability_error"]), "develop") {
		t.Fatalf("scope * was not refused: %v", star)
	}
	if other["capability"] != "" || !strings.Contains(orStr(other["capability_error"]), "ARGUS_ACT_GITLAB_PROJECTS") {
		t.Fatalf("a project off the list was not refused: %v", other)
	}
	if got, _ := spinUpMode(p, "on"); got == "execute" {
		t.Fatal("an injected plan takes the execute path")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestIntakeJobCarriesCatalogue: the intake's task file lists the capabilities from the registry and its allow-lists
// (ADR-0022 D9); with the executor off it is an empty list and mode off.
func TestIntakeJobCarriesCatalogue(t *testing.T) {
	e := agentEnv(t)
	e.run(t)
	job := readJob(t, e, "OPS-7.intake.json")
	if c, ok := job["capabilities"].([]any); !ok || len(c) != 0 || job["actions_mode"] != "off" {
		t.Fatalf("executor off: %v %v", job["capabilities"], job["actions_mode"])
	}
	e2 := agentEnv(t)
	e2.o.Agent.ActionsDir, e2.o.Agent.ActionsMode, e2.o.Agent.Registry = filepath.Join(e2.d, "actions"), "dry-run", recordedRegistry(t)
	e2.run(t)
	job = readJob(t, e2, "OPS-7.intake.json")
	cat, _ := job["capabilities"].([]any)
	if job["actions_mode"] != "dry-run" || len(cat) != 4 {
		t.Fatalf("catalogue %v", job["capabilities"])
	}
	names := []string{}
	for _, c := range cat {
		m := asMap(c)
		names = append(names, orStr(m["name"]))
		if m["name"] == "http.check" {
			hosts := asList(asMap(m["allow_lists"])["ARGUS_ACT_HTTP_HOSTS"])
			if len(hosts) != 1 || hosts[0] != "health.example.com" {
				t.Fatalf("hosts %v", hosts)
			}
		}
	}
	if strings.Join(names, ",") != "gitlab.ci-variable.delete,gitlab.ci-variable.get,gitlab.ci-variable.set,http.check" {
		t.Fatal(names)
	}
}
