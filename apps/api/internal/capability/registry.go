package capability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Registry is the set of capabilities an installation offers. A capability whose allow-list is empty is not in it:
// the intake cannot plan it and actd refuses it.
type Registry struct {
	m      map[string]Capability
	preps  []Preparer
	GitLab *GitLab // the GitLab family when it is offered (its resolved allow-list for the status)
}

// Preparer is a capability family that resolves its allow-list over the network once (actd calls Prepare until
// it succeeds; Validate uses no network).
type Preparer interface {
	Prepare(ctx context.Context) error
}

// Prepare runs every family's Prepare; the first error is returned, the others still run.
func (r *Registry) Prepare(ctx context.Context) error {
	if r == nil {
		return nil
	}
	var first error
	for _, p := range r.preps {
		if err := p.Prepare(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// NewRegistry registers caps; a name used twice is an error.
func NewRegistry(caps ...Capability) (*Registry, error) {
	r := &Registry{m: map[string]Capability{}}
	for _, c := range caps {
		if c == nil {
			continue
		}
		if _, dup := r.m[c.Name()]; dup {
			return nil, fmt.Errorf("capability %s registered twice", c.Name())
		}
		r.m[c.Name()] = c
	}
	return r, nil
}

// Get is the capability of that name.
func (r *Registry) Get(name string) (Capability, bool) {
	if r == nil {
		return nil, false
	}
	c, ok := r.m[name]
	return c, ok
}

// Names is every registered name, sorted.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.m))
	for n := range r.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Options are the allow-lists of the built-in capabilities (from the configuration, never from a request).
type Options struct {
	HTTPHosts      []string          // ARGUS_ACT_HTTP_HOSTS; empty: http.check is not offered
	HTTPAllowPlain bool              // ARGUS_ACT_HTTP_ALLOW_HTTP: plain http for test installs
	Transport      http.RoundTripper // the base transport under the guard (nil: a direct one); tests set it
	// GitLab: the gitlab.ci-variable family; offered when GitLab.Projects is not empty
	GitLab GitLabOptions
	// Keycloak: keycloak.read; offered when Keycloak.URL is not empty
	Keycloak KeycloakOptions
}

// Default is the registry of the built-in capabilities for these allow-lists.
func Default(o Options) (*Registry, error) {
	var caps []Capability
	if len(o.HTTPHosts) > 0 {
		h, err := NewHTTPCheck(o.HTTPHosts, o.HTTPAllowPlain, o.Transport)
		if err != nil {
			return nil, err
		}
		caps = append(caps, h)
	}
	var gl *GitLab
	if len(o.GitLab.Projects) > 0 {
		var err error
		if gl, err = NewGitLab(o.GitLab); err != nil {
			return nil, err
		}
		caps = append(caps, gl.Capabilities()...)
	}
	if strings.TrimSpace(o.Keycloak.URL) != "" {
		kc, err := NewKeycloak(o.Keycloak)
		if err != nil {
			return nil, err
		}
		caps = append(caps, kc)
	}
	r, err := NewRegistry(caps...)
	if err != nil {
		return nil, err
	}
	if gl != nil {
		r.GitLab, r.preps = gl, append(r.preps, gl)
	}
	return r, nil
}

// PlanSHA256 is the hash an approval binds to (ADR-0022 D4): SHA-256 of the canonical JSON (sorted keys) of
// {classification, plan}. The worker computes it at intake, actd again from the stored plan.
func PlanSHA256(classification, plan any) (string, error) {
	b, err := json.Marshal(map[string]any{"classification": classification, "plan": plan})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

var actionIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}\.v[0-9]{1,9}\.[0-9a-f]{12}$`)

// ActionID is "<task key>.v<plan version>.<plan sha256[:12]>" (D3 idempotency); "" when an input is not usable.
func ActionID(taskKey string, version int, planSHA string) string {
	if len(planSHA) < 12 || version <= 0 {
		return ""
	}
	id := fmt.Sprintf("%s.v%d.%s", taskKey, version, planSHA[:12])
	if !actionIDRe.MatchString(id) {
		return ""
	}
	return id
}

// ValidActionID reports whether id has the action id form (it is also a safe file name).
func ValidActionID(id string) bool { return actionIDRe.MatchString(id) }
