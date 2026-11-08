package needs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/config"
)

func lookup(vals map[string]string) func(string) config.Value {
	return func(k string) config.Value {
		if v, ok := vals[k]; ok {
			return config.Value{Raw: v, Source: "env", Set: true}
		}
		return config.Value{Raw: "https://gitlab.example.com", Source: "default", Set: true}
	}
}

func TestMatchLongestPrefix(t *testing.T) {
	for path, want := range map[string]string{"/cicd": "gitlab", "/cicd/audit": "gitlab", "/incidents/rca": "claude",
		"/incidents/view": "alertmanager", "/cicdx": "", "/today": ""} {
		n, ok := Match(Pages, path)
		got := ""
		if ok {
			got = n.AnyOf[0]
		}
		if got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}

func TestDetector(t *testing.T) {
	dir := t.TempDir()
	d := Detector{Active: true, Lookup: lookup(map[string]string{"ARGUS_ALERTS_SOURCES": "a=https://am.example.com/api/v2/alerts"}),
		StatusFile: filepath.Join(dir, "sources-status.json"), JiraFiles: []string{filepath.Join(dir, "jira.env")}, ClaudeEnv: filepath.Join(dir, "claude.env")}
	st := d.Now()
	if !st["alertmanager"] || st["gitlab"] || st["jira"] || st["claude"] || st["sentry"] {
		t.Fatalf("explicit key only: %v", st)
	}
	// a schema default (GITLAB_URL's example) is not a configuration
	if miss := st.Missing([]string{"gitlab"}); len(miss) != 1 || miss[0].Label != "GitLab" {
		t.Fatalf("missing gitlab: %v", miss)
	}
	os.WriteFile(d.StatusFile, []byte(`{"sources":{"sentry/main":{"status":"readonly"}}}`), 0o644)
	os.WriteFile(d.JiraFiles[0], []byte("JIRA_PROJECT=OPS\nJIRA_URL=https://jira.example.com\n"), 0o600)
	os.WriteFile(d.ClaudeEnv, []byte("CLAUDE_CODE_OAUTH_TOKEN=\n"), 0o600)
	st = d.Now()
	if !st["sentry"] || !st["jira"] || st["claude"] {
		t.Fatalf("registry, jira file, empty token: %v", st)
	}
	os.WriteFile(d.ClaudeEnv, []byte("export CLAUDE_CODE_OAUTH_TOKEN='x'\n"), 0o600)
	if !d.Now()["claude"] {
		t.Fatal("claude token not seen")
	}
	if st.Missing([]string{"gitlab", "sentry"}) != nil {
		t.Fatal("any one configured source is enough")
	}
	d.Active = false
	if d.Now().Missing([]string{"gitlab"}) != nil {
		t.Fatal("an inactive gate keeps every feature on")
	}
}

func TestReasonAndAnswer(t *testing.T) {
	k, _ := KindOf("alertmanager")
	if r := Reason([]Kind{k}); !strings.Contains(r, "connect Alertmanager or Prometheus") || !strings.Contains(r, "ARGUS_ALERTS_SOURCES") {
		t.Fatal(r)
	}
	a := Answer([]Kind{k})["notConfigured"].(map[string]any)["sources"].([]map[string]string)
	if a[0]["key"] != "alertmanager" || a[0]["settings"] != "/admin/sources" {
		t.Fatal(a)
	}
}

// every need names a known kind, and every job and page gate is reachable
func TestNeedsNameKnownKinds(t *testing.T) {
	all := append(append([]Need{}, Pages...), APIs...)
	for _, js := range Jobs {
		all = append(all, Need{"job", js})
	}
	for _, n := range all {
		for _, k := range n.AnyOf {
			if _, ok := KindOf(k); !ok {
				t.Errorf("%s needs unknown kind %q", n.Path, k)
			}
		}
	}
}

// every gated page of the frontend calls the shared not-configured state with its own path, and only those do
func TestPagesCallTheGate(t *testing.T) {
	base := filepath.Join("..", "..", "..", "web", "app", "(app)")
	if _, err := os.Stat(base); err != nil {
		t.Skip("frontend not in this checkout")
	}
	filepath.WalkDir(base, func(p string, de os.DirEntry, err error) error {
		if err != nil || de.Name() != "page.tsx" {
			return nil
		}
		rel, _ := filepath.Rel(base, filepath.Dir(p))
		page := "/" + filepath.ToSlash(rel)
		if strings.Contains(page, "[") || strings.HasPrefix(page, "/@") {
			return nil
		}
		b, _ := os.ReadFile(p)
		_, gated := Match(Pages, page)
		calls := strings.Contains(string(b), `notConfigured("`+page+`"`)
		if gated != calls {
			t.Errorf("%s: gated %v, calls notConfigured(%q) %v", page, gated, page, calls)
		}
		return nil
	})
}
