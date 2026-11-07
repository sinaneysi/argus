// Package claudegate is the egress-country guard in front of every Claude run Argus starts.
//
// Before a run starts, Guard looks up the public country the Claude traffic leaves from, with the same proxy
// variables the Claude CLI gets (HTTPS_PROXY, HTTP_PROXY, NO_PROXY of the run's environment, never the caller's own),
// from https://www.cloudflare.com/cdn-cgi/trace (ip=, loc=) and, only when that fails, https://api.country.is/.
// When the country is on the blocked list (default IR) the run is not started: the reason is
// "AI stopped: egress IP <ip> is in <country>". The provider does not serve that region and calls from it can get the
// account disabled. A failed lookup stops the run too (fail closed, the default) or lets it start (fail open).
// The answer is cached in a small JSON file (10 minutes, 1 minute after a failed lookup, keyed by the proxy route)
// so a burst of runs does one lookup. This is a guard, never a bypass: nothing here changes the route or the
// location the traffic leaves from.
package claudegate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultURLs are the lookup sources, in order: the second is asked only when the first gives no country.
var DefaultURLs = []string{"https://www.cloudflare.com/cdn-cgi/trace", "https://api.country.is/"}

const (
	TTL     = 10 * time.Minute // a found country is reused this long
	FailTTL = time.Minute      // a failed lookup is retried after this
	Timeout = 5 * time.Second  // per source
)

var (
	countryRe = regexp.MustCompile(`^[A-Z]{2}$`)
	ipRe      = regexp.MustCompile(`^[0-9A-Fa-f:.]{2,45}$`)
)

// Settings are the guard's settings (the root-written settings file, else the defaults).
type Settings struct {
	Guard   bool     `json:"guard"`
	Blocked []string `json:"blocked"`
	Fail    string   `json:"fail"` // "closed" or "open"
}

// Defaults: guard on, IR blocked, fail closed.
func Defaults() Settings { return Settings{Guard: true, Blocked: []string{"IR"}, Fail: "closed"} }

// Normalize validates s: an error names the first bad value; Blocked is sorted and de-duplicated.
func (s Settings) Normalize() (Settings, error) {
	if s.Fail != "closed" && s.Fail != "open" {
		return s, errors.New("fail must be closed or open")
	}
	if len(s.Blocked) > 50 {
		return s, errors.New("at most 50 blocked countries")
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range s.Blocked {
		if !countryRe.MatchString(c) {
			return s, fmt.Errorf("blocked countries are two-letter codes, got %q", c)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	s.Blocked = out
	return s, nil
}

// LoadSettings reads the settings file; missing or unreadable: the defaults (the guard stays on). Each valid value
// overrides its default.
func LoadSettings(path string) Settings {
	s := Defaults()
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 8192 {
		return s
	}
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return s
	}
	if g, ok := raw["guard"].(bool); ok {
		s.Guard = g
	}
	if l, ok := raw["blocked"].([]any); ok && len(l) <= 50 {
		var bl []string
		good := true
		for _, x := range l {
			c, ok := x.(string)
			if !ok || !countryRe.MatchString(c) {
				good = false
				break
			}
			bl = append(bl, c)
		}
		if good {
			s.Blocked = append([]string{}, bl...)
		}
	}
	if f, ok := raw["fail"].(string); ok && (f == "closed" || f == "open") {
		s.Fail = f
	}
	return s
}

// Found is one lookup's answer: IP and Loc, or Error.
type Found struct {
	IP, Loc, Source, Error string
}

// Check is the cached check and its verdict (the JSON file the web app shows).
type Check struct {
	At            int64    `json:"at"`
	CheckedAt     int64    `json:"checked_at"`
	URLs          []string `json:"urls"`
	Route         string   `json:"route"`
	IP            string   `json:"ip"`
	Loc           string   `json:"loc"`
	Source        string   `json:"source"`
	Error         string   `json:"error"`
	Allowed       bool     `json:"allowed"`
	Reason        string   `json:"reason"`
	Guard         bool     `json:"guard"`
	Blocked       []string `json:"blocked"`
	Fail          string   `json:"fail"`
	Stopped       int      `json:"stopped"`
	LastStoppedAt int64    `json:"last_stopped_at,omitempty"`
}

// ParseEgress is (ip, country) from a Cloudflare trace (ip=, loc=) or a JSON answer ({"ip", "country"}); ("", "")
// when neither has a country.
func ParseEgress(body []byte) (string, string) {
	text := string(body)
	ip, loc := "", ""
	if strings.Contains(text, "loc=") {
		for _, line := range strings.Split(text, "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
			switch k {
			case "ip":
				ip = strings.TrimSpace(v)
			case "loc":
				loc = strings.ToUpper(strings.TrimSpace(v))
			}
		}
	} else {
		var doc struct {
			IP      any `json:"ip"`
			Country any `json:"country"`
		}
		if json.Unmarshal(body, &doc) == nil {
			ip, _ = doc.IP.(string)
			c, _ := doc.Country.(string)
			loc = strings.ToUpper(c)
		}
	}
	if !countryRe.MatchString(loc) {
		return "", ""
	}
	if !ipRe.MatchString(ip) {
		ip = "?"
	}
	return ip, loc
}

func envGet(env map[string]string, name string) string {
	if v := env[name]; v != "" {
		return v
	}
	return env[strings.ToLower(name)]
}

// proxyFor is the proxy env gives for u (nil: direct), the same rules as the Claude CLI's HTTPS_PROXY/NO_PROXY.
func proxyFor(env map[string]string, u *url.URL) (*url.URL, error) {
	p := envGet(env, strings.ToUpper(u.Scheme)+"_PROXY")
	if p == "" {
		return nil, nil
	}
	host := strings.ToLower(u.Hostname())
	for _, e := range strings.Split(envGet(env, "NO_PROXY"), ",") {
		e = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(e)), ".")
		if e == "*" || (e != "" && (host == e || strings.HasSuffix(host, "."+e))) {
			return nil, nil
		}
	}
	return url.Parse(p)
}

// Lookup asks the sources in order with env's proxies only; the first country wins.
func Lookup(env map[string]string, urls []string, timeout time.Duration) Found {
	var errs []string
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			errs = append(errs, "bad source URL")
			continue
		}
		client := &http.Client{Timeout: timeout, Transport: &http.Transport{
			Proxy: func(r *http.Request) (*url.URL, error) { return proxyFor(env, r.URL) }},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, _ := http.NewRequest("GET", raw, nil)
		req.Header.Set("User-Agent", "argus-egress-check")
		resp, err := client.Do(req)
		if err != nil {
			errs = append(errs, u.Hostname()+": no answer")
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if ip, loc := ParseEgress(body); loc != "" {
			return Found{IP: ip, Loc: loc, Source: u.Hostname()}
		}
		errs = append(errs, u.Hostname()+": no country in the answer")
	}
	e := strings.Join(errs, "; ")
	if e == "" {
		e = "no source configured"
	}
	if len(e) > 300 {
		e = e[:300]
	}
	return Found{Error: e}
}

// Verdict is (allowed, reason) for an answer under the settings.
func Verdict(f Found, s Settings) (bool, string) {
	if f.Loc != "" {
		for _, b := range s.Blocked {
			if b == f.Loc {
				return false, "AI stopped: egress IP " + orQ(f.IP) + " is in " + f.Loc
			}
		}
		return true, ""
	}
	e := f.Error
	if e == "" {
		e = "no answer"
	}
	if s.Fail == "open" {
		return true, "egress country unknown (" + e + "); fail-open lets the run start"
	}
	return false, "AI stopped: egress country unknown (" + e + "); fail-closed"
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// RouteID names the way the traffic leaves: "direct", or a short hash of the proxy settings (never stored in clear).
func RouteID(env map[string]string) string {
	vals := []string{envGet(env, "HTTPS_PROXY"), envGet(env, "HTTP_PROXY"), envGet(env, "NO_PROXY")}
	if strings.Join(vals, "") == "" {
		return "direct"
	}
	h := sha256.Sum256([]byte(strings.Join(vals, "\n")))
	return "proxy-" + hex.EncodeToString(h[:])[:12]
}

// Gate is one guard: where its settings and cache live, the run's environment, and (tests) a clock and a lookup.
type Gate struct {
	SettingsFile string
	CacheFile    string
	URLs         []string // empty: DefaultURLs
	Env          map[string]string
	Now          func() time.Time
	LookupFn     func(env map[string]string, urls []string) Found
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// ReadCheck is the cached check (zero when none).
func ReadCheck(path string) Check {
	var c Check
	if b, err := os.ReadFile(path); err == nil && len(b) < 16384 {
		json.Unmarshal(b, &c)
	}
	return c
}

func writeCheck(path string, c Check) {
	b, _ := json.MarshalIndent(c, "", " ")
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return // best effort: a user that cannot write it still gets its verdict, only uncached
	}
	os.Chmod(tmp, 0o644)
	if os.Rename(tmp, path) != nil {
		os.Remove(tmp)
	}
}

// Guard is (allowed, reason, check) for a run that would start under g.Env. Guard off: allowed, no lookup. A fresh
// cached check for the same sources and route is reused unless force; the verdict always uses the current settings.
// A stop is counted in the cache.
func (g *Gate) Guard(force bool) (bool, string, Check) {
	s := LoadSettings(g.SettingsFile)
	if !s.Guard {
		return true, "", Check{Guard: false}
	}
	urls := g.URLs
	if len(urls) == 0 {
		urls = DefaultURLs
	}
	now := g.now().Unix()
	route := RouteID(g.Env)
	c := ReadCheck(g.CacheFile)
	ttl := int64(FailTTL / time.Second)
	if c.Loc != "" {
		ttl = int64(TTL / time.Second)
	}
	fresh := c.At > 0 && now-c.At >= 0 && now-c.At < ttl && strings.Join(c.URLs, " ") == strings.Join(urls, " ") && c.Route == route
	if force || !fresh {
		lookup := g.LookupFn
		if lookup == nil {
			lookup = func(env map[string]string, urls []string) Found { return Lookup(env, urls, Timeout) }
		}
		f := lookup(g.Env, urls)
		c = Check{At: now, URLs: urls, Route: route, IP: f.IP, Loc: f.Loc, Source: f.Source, Error: f.Error,
			Stopped: c.Stopped, LastStoppedAt: c.LastStoppedAt}
	}
	ok, reason := Verdict(Found{IP: c.IP, Loc: c.Loc, Source: c.Source, Error: c.Error}, s)
	c.Allowed, c.Reason, c.Guard, c.Blocked, c.Fail, c.CheckedAt = ok, reason, true, s.Blocked, s.Fail, now
	if !ok {
		c.Stopped++
		c.LastStoppedAt = now
	}
	if force || !fresh || !ok {
		if dir := filepath.Dir(g.CacheFile); dir != "" {
			writeCheck(g.CacheFile, c)
		}
	}
	return ok, reason, c
}
