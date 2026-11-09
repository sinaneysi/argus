// Package needs is the data-source registry of features (ADR-0021 D5): which data source each page, API route and
// worker job needs, and whether that source is configured. A feature whose sources are all missing is "not
// configured": its page shows one shared "Connect X to enable this page" state, its API route answers HTTP 200 with
// {"notConfigured": {...}}, and its worker job does not run and says why.
//
// A source counts as configured when Settings -> Data sources holds an entry of its type (the registry status file),
// or when the configuration sets its key explicitly (automation and the legacy layout), or for Jira and Claude when
// their settings file holds the key. Nothing here reads or returns a secret value: only whether a key is non-empty.
//
// The gate is active only in ARGUS_LAYOUT=volume outside the demo. The legacy layout keeps its host jobs and its
// configuration in host files that the web process cannot always read, so there every feature stays on.
package needs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"github.com/sinaneysi/argus/internal/config"
)

// Kind is one data-source type a feature can need.
type Kind struct {
	Key, Label string
	Settings   string   // the page where it is connected
	Keys       []string // configuration keys: one set explicitly means configured
	Registry   string   // its type in Settings -> Data sources ("" when it has none)
	Hint       string   // how automation configures it instead
}

// Kinds are the data-source types, in Settings order.
var Kinds = []Kind{
	{Key: "gitlab", Label: "GitLab", Settings: "/admin/sources", Keys: []string{"GITLAB_URL"}, Registry: "gitlab", Hint: "or set GITLAB_URL"},
	{Key: "jira", Label: "Jira", Settings: "/admin/sources", Registry: "jira", Hint: "or set JIRA_URL in the board's Jira settings file"},
	{Key: "alertmanager", Label: "Alertmanager or Prometheus", Settings: "/admin/sources", Keys: []string{"ARGUS_ALERTS_SOURCES"}, Registry: "alertmanager", Hint: "or set ARGUS_ALERTS_SOURCES"},
	{Key: "grafana", Label: "Grafana", Settings: "/admin/sources", Registry: "grafana"},
	{Key: "kubernetes", Label: "Kubernetes", Settings: "/admin/kube", Registry: "kubernetes"},
	{Key: "outline", Label: "Outline", Settings: "/admin/sources", Keys: []string{"OUTLINE_URL"}, Registry: "outline", Hint: "or set OUTLINE_URL"},
	{Key: "elasticsearch", Label: "Elasticsearch / OpenSearch", Settings: "/admin/sources", Registry: "elasticsearch"},
	{Key: "sentry", Label: "Sentry", Settings: "/admin/sources", Registry: "sentry"},
	{Key: "matrix", Label: "Matrix", Settings: "/admin/sources", Keys: []string{"MATRIX_HOMESERVER"}, Registry: "matrix", Hint: "or set MATRIX_HOMESERVER"},
	{Key: "claude", Label: "Claude", Settings: "/admin/claude", Registry: "anthropic"},
	{Key: "teleport", Label: "Teleport", Settings: "/admin/sources", Keys: []string{"ARGUS_TELEPORT_PROXY"}, Registry: "teleport", Hint: "or set ARGUS_TELEPORT_PROXY"},
}

// KindOf is the kind with this key.
func KindOf(key string) (Kind, bool) {
	for _, k := range Kinds {
		if k.Key == key {
			return k, true
		}
	}
	return Kind{}, false
}

// Need is one feature (a page or an API route, by path prefix) and the sources it needs: any one of AnyOf is enough.
type Need struct {
	Path  string
	AnyOf []string
}

// Pages are the frontend pages that need a data source. A path covers its sub-pages; the longest match wins.
var Pages = []Need{
	{"/alerts", []string{"alertmanager"}},
	{"/incidents", []string{"alertmanager", "matrix"}},
	{"/incidents/rca", []string{"claude"}},
	{"/cicd", []string{"gitlab"}},
	{"/qa", []string{"matrix"}},
	{"/rca", []string{"claude"}},
	{"/admin/fix", []string{"claude"}},
}

// APIs are the API GET routes that need a data source (same matching as Pages). Writes are not gated: they are
// refused by their own checks.
var APIs = []Need{
	{"/api/v1/alerts", []string{"alertmanager"}},
	{"/api/v1/incidents", []string{"alertmanager", "matrix"}},
	{"/api/v1/incidents/report", []string{"claude"}},
	{"/api/v1/cicd", []string{"gitlab"}},
	{"/api/v1/qa", []string{"matrix"}},
	{"/api/v1/rca", []string{"claude"}},
	{"/api/v1/fix", []string{"claude"}},
}

// Jobs are the worker jobs that need a data source, by job name.
var Jobs = map[string][]string{
	"collect-alerts": {"alertmanager"},
	"incidents":      {"alertmanager"},
	"inventory":      {"grafana", "alertmanager"},
	"vm-pulse":       {"grafana", "alertmanager"},
	"cicd-watch":     {"gitlab"},
	"cicd-daily":     {"gitlab"},
	"task-agent":     {"claude"},
	"ideas":          {"claude"},
}

// Match is the need of the longest entry that covers path; ok false when none does.
func Match(list []Need, path string) (Need, bool) {
	best, ok := Need{}, false
	for _, n := range list {
		if (path == n.Path || strings.HasPrefix(path, n.Path+"/")) && len(n.Path) > len(best.Path) {
			best, ok = n, true
		}
	}
	return best, ok
}

// Detector says whether a kind is configured.
type Detector struct {
	Active     bool                      // false: every kind counts as configured (legacy layout, demo)
	Lookup     func(string) config.Value // the configuration
	StatusFile string                    // Settings -> Data sources status (settings-out/sources-status.json)
	JiraFiles  []string                  // the board's Jira settings files, first wins
	ClaudeEnv  string                    // the Claude env file (CLAUDE_CODE_OAUTH_TOKEN)
}

// FromConfig builds the detector of a loaded configuration. demo turns the gate off.
func FromConfig(lookup func(string) config.Value, demo bool) Detector {
	get := func(k string) string { return lookup(k).Raw }
	jira := []string{}
	for _, f := range strings.Split(get("ARGUS_BOARD_ENV_FILES"), ":") {
		if f = strings.TrimSpace(f); f != "" {
			jira = append(jira, f)
		}
	}
	if len(jira) == 0 && get("ARGUS_ETC_DIR") != "" {
		jira = []string{strings.TrimRight(get("ARGUS_ETC_DIR"), "/") + "/jira.env"}
	}
	return Detector{
		Active:     get("ARGUS_LAYOUT") == "volume" && !demo,
		Lookup:     lookup,
		StatusFile: strings.TrimRight(get("ARGUS_SETTINGS_OUT"), "/") + "/sources-status.json",
		JiraFiles:  jira,
		ClaudeEnv:  get("ARGUS_CLAUDE_ENV_FILE"),
	}
}

// registryTypes are the types with at least one entry in the Data sources status file.
func (d Detector) registryTypes() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(d.StatusFile)
	if err != nil || d.StatusFile == "" {
		return out
	}
	var st struct {
		Sources map[string]json.RawMessage `json:"sources"`
	}
	if json.Unmarshal(b, &st) != nil {
		return out
	}
	for k := range st.Sources {
		if t, _, ok := strings.Cut(k, "/"); ok {
			out[t] = true
		}
	}
	return out
}

// fileHasKey: the KEY=value file sets key to a non-empty value (the value itself is never kept).
func fileHasKey(path, key string) bool {
	if path == "" {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		if ok && k == key && strings.Trim(strings.TrimSpace(v), `"'`) != "" {
			return true
		}
	}
	return false
}

// State is whether every kind is configured.
type State map[string]bool

// Now reads the configured state of every kind once.
func (d Detector) Now() State {
	st := State{}
	if !d.Active {
		for _, k := range Kinds {
			st[k.Key] = true
		}
		return st
	}
	reg := d.registryTypes()
	for _, k := range Kinds {
		ok := k.Registry != "" && reg[k.Registry]
		for _, key := range k.Keys {
			if d.Lookup == nil {
				break
			}
			if v := d.Lookup(key); v.Set && v.Source != "default" && strings.TrimSpace(v.Raw) != "" {
				ok = true
			}
		}
		switch k.Key {
		case "jira":
			for _, f := range d.JiraFiles {
				if fileHasKey(f, "JIRA_URL") {
					ok = true
				}
			}
		case "claude":
			ok = ok || fileHasKey(d.ClaudeEnv, "CLAUDE_CODE_OAUTH_TOKEN")
		}
		st[k.Key] = ok
	}
	return st
}

// Missing is the kinds of anyOf when none of them is configured; nil when one is (or anyOf is empty).
func (s State) Missing(anyOf []string) []Kind {
	var out []Kind
	for _, key := range anyOf {
		if s[key] {
			return nil
		}
		if k, ok := KindOf(key); ok {
			out = append(out, k)
		}
	}
	return out
}

// Labels is "GitLab" or "Alertmanager or Prometheus, or Grafana".
func Labels(ks []Kind) string {
	var parts []string
	for _, k := range ks {
		parts = append(parts, k.Label)
	}
	return strings.Join(parts, ", or ")
}

// Reason is the worker's one line for a job whose sources are missing.
func Reason(ks []Kind) string {
	if len(ks) == 0 {
		return ""
	}
	r := "not configured: connect " + Labels(ks) + " in Settings → Data sources"
	if ks[0].Hint != "" {
		r += " (" + ks[0].Hint + ")"
	}
	return r
}

// Answer is the {"notConfigured": ...} body of a gated API route and of GET /api/v1/needs.
func Answer(ks []Kind) map[string]any {
	src := []map[string]string{}
	for _, k := range ks {
		src = append(src, map[string]string{"key": k.Key, "label": k.Label, "settings": k.Settings, "hint": k.Hint})
	}
	return map[string]any{"notConfigured": map[string]any{"sources": src}}
}
