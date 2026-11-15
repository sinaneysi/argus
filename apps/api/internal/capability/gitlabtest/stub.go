// Package gitlabtest is a GitLab stub for the tests of the gitlab.ci-variable capabilities: project lookups and the
// project variables API (scope filter required), every call recorded. Synthetic data only; used by tests only.
package gitlabtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sinaneysi/argus/internal/capability"
)

// Prefix is the stub's path prefix (the capability must keep a GitLab URL's path).
const Prefix = "/gl"

// URL is the GitLab URL the capability is configured with (plain http, dialled to the stub).
const URL = "http://gitlab.example.test" + Prefix

// Token is the stub's accepted token (a fake one).
var Token = "test-" + "token-" + "0001"

// Var is one stored variable.
type Var struct {
	Value     string
	Type      string
	Protected bool
	Masked    bool
	Raw       bool
	Hidden    bool
}

// Call is one recorded request.
type Call struct {
	Method, Path, Query, Body string
}

// Stub is the fake GitLab.
type Stub struct {
	Srv      *httptest.Server
	mu       sync.Mutex
	Projects map[int64]string // id -> path
	Vars     map[string]Var   // "<id>/<key>"
	calls    []Call           // every request
	AfterW   func(s *Stub)    // runs after a successful write (e.g. to change the value behind the capability)
	Status   map[string]int   // "<METHOD>" -> forced status for writes
}

// New starts the stub with two projects: 101 shop/checkout and 102 shop/catalog.
func New(t *testing.T) *Stub {
	s := &Stub{Projects: map[int64]string{101: "shop/checkout", 102: "shop/catalog"}, Vars: map[string]Var{}, Status: map[string]int{}}
	s.Srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Srv.Close)
	return s
}

// Transport dials the stub whatever host the URL names.
func (s *Stub) Transport() http.RoundTripper {
	addr := s.Srv.Listener.Addr().String()
	return &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}
}

// Calls is a copy of the recorded requests.
func (s *Stub) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Writes are the recorded POST, PUT and DELETE requests.
func (s *Stub) Writes() []Call {
	var out []Call
	for _, c := range s.Calls() {
		if c.Method != http.MethodGet {
			out = append(out, c)
		}
	}
	return out
}

// Reset forgets the recorded requests.
func (s *Stub) Reset() {
	s.mu.Lock()
	s.calls = nil
	s.mu.Unlock()
}

// Get is a stored variable.
func (s *Stub) Get(id int64, key string) (Var, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Vars[fmt.Sprintf("%d/%s", id, key)]
	return v, ok
}

// Put stores a variable.
func (s *Stub) Put(id int64, key string, v Var) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.Type == "" {
		v.Type = "env_var"
	}
	s.Vars[fmt.Sprintf("%d/%s", id, key)] = v
}

func send(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func varJSON(key string, v Var) map[string]any {
	m := map[string]any{"key": key, "variable_type": v.Type, "protected": v.Protected, "masked": v.Masked, "raw": v.Raw,
		"environment_scope": "develop", "hidden": v.Hidden}
	if !v.Hidden {
		m["value"] = v.Value
	} else {
		m["value"] = nil
	}
	return m
}

func (s *Stub) serve(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
	}
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Body: string(body)})
	s.mu.Unlock()
	if r.Header.Get("PRIVATE-TOKEN") != Token {
		send(w, 401, map[string]any{"message": "401 Unauthorized"})
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), Prefix+"/api/v4/projects/")
	if !ok {
		send(w, 404, nil)
		return
	}
	seg := strings.Split(rest, "/")
	if len(seg) == 1 && r.Method == http.MethodGet { // project lookup by id or encoded path
		ref, _ := url.PathUnescape(seg[0])
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, p := range s.Projects {
			if strconv.FormatInt(id, 10) == ref || p == strings.ToLower(ref) {
				send(w, 200, map[string]any{"id": id, "path_with_namespace": p})
				return
			}
		}
		send(w, 404, map[string]any{"message": "404 Project Not Found"})
		return
	}
	id, err := strconv.ParseInt(seg[0], 10, 64)
	if err != nil || len(seg) < 2 || seg[1] != "variables" {
		send(w, 418, map[string]any{"message": "not a variables call"})
		return
	}
	var in map[string]any
	if len(body) > 0 {
		json.Unmarshal(body, &in)
	}
	attrs := func(v Var) Var {
		if x, ok := in["value"].(string); ok {
			v.Value = x
		}
		if x, ok := in["variable_type"].(string); ok {
			v.Type = x
		}
		for k, p := range map[string]*bool{"protected": &v.Protected, "masked": &v.Masked, "raw": &v.Raw} {
			if x, ok := in[k].(bool); ok {
				*p = x
			}
		}
		return v
	}
	if c := s.Status[r.Method]; c != 0 && r.Method != http.MethodGet {
		send(w, c, map[string]any{"message": "forced"})
		return
	}
	switch {
	case len(seg) == 2 && r.Method == http.MethodPost:
		key, _ := in["key"].(string)
		if in["environment_scope"] != "develop" {
			send(w, 400, map[string]any{"message": "scope"})
			return
		}
		if _, ok := s.Get(id, key); ok {
			send(w, 400, map[string]any{"message": "key has already been taken"})
			return
		}
		v := attrs(Var{Type: "env_var"})
		s.Put(id, key, v)
		send(w, 201, varJSON(key, v))
	case len(seg) == 3:
		key := seg[2]
		if r.URL.Query().Get("filter[environment_scope]") != "develop" {
			send(w, 409, map[string]any{"message": "no scope filter"})
			return
		}
		v, ok := s.Get(id, key)
		switch r.Method {
		case http.MethodGet:
			if !ok {
				send(w, 404, map[string]any{"message": "404 Variable Not Found"})
				return
			}
			send(w, 200, varJSON(key, v))
			return
		case http.MethodPut:
			if !ok {
				send(w, 404, map[string]any{"message": "404 Variable Not Found"})
				return
			}
			v = attrs(v)
			s.Put(id, key, v)
			send(w, 200, varJSON(key, v))
		case http.MethodDelete:
			if !ok {
				send(w, 404, map[string]any{"message": "404 Variable Not Found"})
				return
			}
			s.mu.Lock()
			delete(s.Vars, fmt.Sprintf("%d/%s", id, key))
			s.mu.Unlock()
			w.WriteHeader(204)
		default:
			send(w, 405, nil)
			return
		}
	default:
		send(w, 405, nil)
		return
	}
	if s.AfterW != nil {
		s.AfterW(s)
	}
}

// Backups is an in-memory capability.Backups that records the order of its calls next to the stub's.
type Backups struct {
	mu    sync.Mutex
	m     map[string][2]string
	Order []string // "backup <ref>" entries, appended when Put runs
	Stub  *Stub
}

// Put keeps the backup and records, with the number of writes the stub had seen, that it ran.
func (b *Backups) Put(r capability.BackupRef, meta string, plain []byte) error {
	ref := string(r)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string][2]string{}
	}
	if _, ok := b.m[ref]; !ok {
		b.m[ref] = [2]string{meta, string(plain)}
	}
	n := 0
	if b.Stub != nil {
		n = len(b.Stub.Writes())
	}
	b.Order = append(b.Order, fmt.Sprintf("backup %s after %d writes", ref, n))
	return nil
}

// Get returns a kept backup.
func (b *Backups) Get(r capability.BackupRef) (string, []byte, error) {
	ref := string(r)
	b.mu.Lock()
	defer b.mu.Unlock()
	x, ok := b.m[ref]
	if !ok {
		return "", nil, fmt.Errorf("no backup")
	}
	return x[0], []byte(x[1]), nil
}
