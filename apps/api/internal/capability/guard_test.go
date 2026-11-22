package capability

import (
	"errors"
	"net/http"
	"testing"
)

type recordRT struct{ n int }

func (r *recordRT) RoundTrip(*http.Request) (*http.Response, error) {
	r.n++
	return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
}

func TestGuardRefusesBeforeConnection(t *testing.T) {
	base := &recordRT{}
	host := func(h string) bool { return h == "git.example.test" }
	g := &Guard{Base: base, NoCredentials: true, Rules: []Rule{
		{Method: "GET", Path: "/api/v4/projects/:id/variables", Host: host},
		{Method: "GET", Path: "/api/v4/projects/:id/variables/:key", Host: host},
		{Method: "PUT", Path: "/api/v4/projects/:id/variables/:key", Host: host},
	}}
	allowed := []struct{ m, u string }{
		{"GET", "https://git.example.test/api/v4/projects/12/variables"},
		{"GET", "https://git.example.test/api/v4/projects/12/variables/KEY"},
		{"PUT", "https://git.example.test/api/v4/projects/12/variables/KEY"},
	}
	for _, c := range allowed {
		req, _ := http.NewRequest(c.m, c.u, nil)
		if _, err := g.RoundTrip(req); err != nil {
			t.Errorf("%s %s refused: %v", c.m, c.u, err)
		}
	}
	refused := []struct{ m, u string }{
		{"POST", "https://git.example.test/api/v4/projects/12/pipeline"},
		{"POST", "https://git.example.test/api/v4/projects/12/jobs/5/play"},
		{"DELETE", "https://git.example.test/api/v4/projects/12/variables/KEY"},
		{"GET", "https://git.example.test/api/v4/projects/12/variables/KEY/extra"},
		{"GET", "https://git.example.test/api/v4/projects/12/variables/../jobs"},
		{"GET", "https://git.example.test/api/v4/projects/a%2Fb/variables"},
		{"GET", "https://other.example.test/api/v4/projects/12/variables"},
		{"GET", "https://" + userInfo + "git.example.test/api/v4/projects/12/variables"},
	}
	for _, c := range refused {
		req, _ := http.NewRequest(c.m, c.u, nil)
		_, err := g.RoundTrip(req)
		var ge *ErrGuard
		if !errors.As(err, &ge) {
			t.Errorf("%s %s not refused: %v", c.m, c.u, err)
		}
	}
	req, _ := http.NewRequest("GET", "https://git.example.test/api/v4/projects/12/variables", nil)
	req.Header.Set("Authorization", "Bearer x")
	if _, err := g.RoundTrip(req); err == nil {
		t.Error("a credential header passed a NoCredentials guard")
	}
	if base.n != len(allowed) {
		t.Fatalf("the base transport saw %d requests, want %d: a refused request reached it", base.n, len(allowed))
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		tmpl, path string
		want       bool
	}{
		{"/**", "/", true}, {"/**", "/a/b", true}, {"/a/:x", "/a/1", true}, {"/a/:x", "/a/", false},
		{"/a/:x", "/a/1/2", false}, {"/a/**", "/a", true}, {"/a", "/a/..", false}, {"/**", "/a/%2F", false},
	}
	for _, c := range cases {
		if got := MatchPath(c.tmpl, c.path); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v", c.tmpl, c.path, got)
		}
	}
}
