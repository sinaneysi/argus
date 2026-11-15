package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
)

// Registry is the set of capabilities an installation offers. A capability whose allow-list is empty is not in it:
// the intake cannot plan it and actd refuses it.
type Registry struct {
	m map[string]Capability
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
	return NewRegistry(caps...)
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
