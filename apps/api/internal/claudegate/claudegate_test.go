package claudegate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gate(t *testing.T, found Found) (*Gate, *int) {
	t.Helper()
	d := t.TempDir()
	n := 0
	g := &Gate{SettingsFile: filepath.Join(d, "egress.json"), CacheFile: filepath.Join(d, "check.json"), Env: map[string]string{},
		LookupFn: func(map[string]string, []string) Found { n++; return found }}
	return g, &n
}

func TestSettings(t *testing.T) {
	g, _ := gate(t, Found{})
	if s := LoadSettings(g.SettingsFile); !s.Guard || strings.Join(s.Blocked, ",") != "IR" || s.Fail != "closed" {
		t.Fatalf("defaults: %+v", s)
	}
	os.WriteFile(g.SettingsFile, []byte(`{"guard": false, "blocked": ["IR", "KP"], "fail": "open"}`), 0o644)
	if s := LoadSettings(g.SettingsFile); s.Guard || len(s.Blocked) != 2 || s.Fail != "open" {
		t.Fatalf("file: %+v", s)
	}
	os.WriteFile(g.SettingsFile, []byte(`{"guard": "no", "blocked": ["iran"], "fail": "x"}`), 0o644)
	if s := LoadSettings(g.SettingsFile); !s.Guard || s.Blocked[0] != "IR" || s.Fail != "closed" {
		t.Fatalf("bad values keep their defaults: %+v", s)
	}
	os.WriteFile(g.SettingsFile, []byte(`{broken`), 0o644)
	if !LoadSettings(g.SettingsFile).Guard {
		t.Fatal("unreadable: the guard stays on")
	}
	if s, err := (Settings{Guard: true, Blocked: []string{"KP", "IR", "IR"}, Fail: "open"}).Normalize(); err != nil || strings.Join(s.Blocked, ",") != "IR,KP" {
		t.Fatalf("normalize: %v %v", s, err)
	}
	for _, bad := range []Settings{{Fail: "x"}, {Fail: "closed", Blocked: []string{"IRN"}}} {
		if _, err := bad.Normalize(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestParse(t *testing.T) {
	if ip, loc := ParseEgress([]byte("fl=1\nip=203.0.113.9\nloc=IR\ncolo=AMS\n")); ip != "203.0.113.9" || loc != "IR" {
		t.Fatal(ip, loc)
	}
	if ip, loc := ParseEgress([]byte(`{"ip":"198.51.100.4","country":"NL"}`)); ip != "198.51.100.4" || loc != "NL" {
		t.Fatal(ip, loc)
	}
	if _, loc := ParseEgress([]byte("<html>")); loc != "" {
		t.Fatal(loc)
	}
	if ip, _ := ParseEgress([]byte("ip=<x>\nloc=NL")); ip != "?" {
		t.Fatal(ip)
	}
}

func TestBlockedAllowedAndFailModes(t *testing.T) {
	g, _ := gate(t, Found{IP: "203.0.113.9", Loc: "IR", Source: "x"})
	ok, reason, c := g.Guard(false)
	if ok || reason != "AI stopped: egress IP 203.0.113.9 is in IR" || c.Stopped != 1 {
		t.Fatalf("IR: %v %q %+v", ok, reason, c)
	}
	if st, _ := os.Stat(g.CacheFile); st == nil || st.Mode().Perm() != 0o644 {
		t.Fatal("cache file")
	}
	g, _ = gate(t, Found{IP: "198.51.100.4", Loc: "NL"})
	if ok, _, _ := g.Guard(false); !ok {
		t.Fatal("NL refused")
	}
	g, _ = gate(t, Found{Error: "down"})
	if ok, reason, _ := g.Guard(false); ok || !strings.Contains(reason, "fail-closed") {
		t.Fatal("fail closed")
	}
	os.WriteFile(g.SettingsFile, []byte(`{"fail": "open"}`), 0o644)
	if ok, reason, _ := g.Guard(true); !ok || !strings.Contains(reason, "fail-open") {
		t.Fatal("fail open")
	}
	os.WriteFile(g.SettingsFile, []byte(`{"guard": false}`), 0o644)
	g2, n := gate(t, Found{Loc: "IR"})
	g2.SettingsFile = g.SettingsFile
	if ok, _, _ := g2.Guard(false); !ok || *n != 0 {
		t.Fatal("guard off: allowed, no lookup")
	}
}

func TestCacheReuse(t *testing.T) {
	g, n := gate(t, Found{IP: "198.51.100.4", Loc: "NL"})
	now := time.Now()
	g.Now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		g.Guard(false)
	}
	if *n != 1 {
		t.Fatalf("a burst: %d lookups", *n)
	}
	g.Guard(true)
	if *n != 2 {
		t.Fatal("force")
	}
	now = now.Add(TTL + time.Second)
	g.Guard(false)
	if *n != 3 {
		t.Fatal("expired")
	}
	os.WriteFile(g.SettingsFile, []byte(`{"blocked": ["NL"]}`), 0o644)
	if ok, _, _ := g.Guard(false); ok || *n != 3 {
		t.Fatal("the verdict uses the current settings without a new lookup")
	}
	g.Env = map[string]string{"HTTPS_PROXY": "http://proxy.example:3128"}
	g.Guard(false)
	if *n != 4 {
		t.Fatal("another route: a new lookup")
	}
	if c := ReadCheck(g.CacheFile); strings.Contains(c.Route, "proxy.example") {
		t.Fatal("the proxy is stored in clear")
	}
}

func TestLookupUsesTheRunsProxyOnly(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.String())
		w.Write([]byte("ip=203.0.113.9\nloc=IR\n"))
	}))
	defer srv.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9") // the caller's own proxy is never used
	f := Lookup(map[string]string{}, []string{srv.URL + "/trace"}, 2*time.Second)
	if f.Loc != "IR" || f.Source != "127.0.0.1" {
		t.Fatalf("direct: %+v", f)
	}
	seen = nil
	f = Lookup(map[string]string{"HTTP_PROXY": srv.URL}, []string{"http://trace.example.invalid/cdn-cgi/trace"}, 2*time.Second)
	if f.Loc != "IR" || len(seen) != 1 || !strings.HasPrefix(seen[0], "http://trace.example.invalid/") {
		t.Fatalf("via the proxy: %+v %v", f, seen)
	}
	f = Lookup(map[string]string{"HTTP_PROXY": srv.URL, "NO_PROXY": "example.invalid"}, []string{"http://trace.example.invalid/x"}, 2*time.Second)
	if f.Error == "" {
		t.Fatal("NO_PROXY: direct, and that name does not resolve")
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("nothing")) }))
	defer empty.Close()
	if f := Lookup(map[string]string{}, []string{empty.URL, srv.URL}, 2*time.Second); f.Loc != "IR" {
		t.Fatal("second source after the first gives no country")
	}
}
