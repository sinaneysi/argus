// Package googlebot is the crawler traffic job of the marketing module (ADR-0021 D6, P6): it reads the edge access
// lines that carry a Google crawler user agent from Loki (through a Grafana data source), classifies every line, and
// stores aggregates and per-URL crawl state in its own database (googlebot.db, migrations under migrate.Googlebot).
// A detector turns complete hours into alerts (gw_alerts); a daily job reads the site's own sitemaps (gw_sitemap).
// The SEO page's Googlebot section (seo_snapshots, seo_errors in ARGUS_DB) is derived from these aggregates
// (seocheck.go), so Loki is read once.
//
// Log content is untrusted: no line reaches a model, URLs are stored decoded and cut to 300 characters, and alert
// texts are built from the structured record only.
//
// parse.go: the pure helpers (histogram, crawler type, status columns, URL normalisation, path classes).
package googlebot

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	urlMax = 300 // characters kept of a normalised URL
	hb     = 1.2 // histogram bin ratio: every bin spans +20 %, so a percentile is within ~10 %; merging is exact
	nb     = 90  // histogram bins
)

// Status are the counter columns of gw_agg, in table order.
var Status = []string{"s2xx", "s301", "s302", "s304", "s3xx", "s4xx", "s403", "s404", "s410", "s429", "s5xx", "s499", "s503", "s504"}

var statusSet = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range Status {
		m[s] = true
	}
	return m
}()

// cacheStates are the cache status values kept; anything else counts as "none".
var cacheStates = map[string]bool{"HIT": true, "MISS": true, "EXPIRED": true, "STALE": true, "BYPASS": true, "UPDATING": true, "none": true}

// queryAllow are the query parameters whose value stays in the normalised URL.
var queryAllow = map[string]bool{"page": true}

// Hist is a log-scale latency histogram: bin (as text, the stored JSON key) -> count.
type Hist map[string]int64

// HBin is the bin of a latency in milliseconds.
func HBin(ms float64) int {
	if ms < 1 {
		return 0
	}
	return min(nb-1, 1+int(math.Log(ms)/math.Log(hb)))
}

func hmid(b int) float64 {
	if b == 0 {
		return 0.5
	}
	return math.Pow(hb, float64(b)-0.5)
}

// Merge adds b into h.
func (h Hist) Merge(b Hist) Hist {
	for k, v := range b {
		h[k] += v
	}
	return h
}

// Quantile is the q quantile in milliseconds (one decimal); ok false for an empty histogram.
func (h Hist) Quantile(q float64) (float64, bool) {
	var n int64
	keys := make([]int, 0, len(h))
	for k, v := range h {
		n += v
		if b, err := strconv.Atoi(k); err == nil {
			keys = append(keys, b)
		}
	}
	if n == 0 {
		return 0, false
	}
	sort.Ints(keys)
	want, seen := q*float64(n), int64(0)
	for _, b := range keys {
		seen += h[strconv.Itoa(b)]
		if float64(seen) >= want {
			return math.Round(hmid(b)*10) / 10, true
		}
	}
	return 0, false
}

// ParseHist reads a stored histogram ({} on bad JSON).
func ParseHist(s string) Hist {
	h := Hist{}
	json.Unmarshal([]byte(s), &h)
	return h
}

// Crawler is the crawler type of a user agent.
func Crawler(ua string) string {
	switch {
	case strings.Contains(ua, "Googlebot-Image"):
		return "image"
	case strings.Contains(ua, "Googlebot-Video"):
		return "video"
	case strings.Contains(ua, "AdsBot-Google"):
		return "adsbot"
	case strings.Contains(ua, "Storebot-Google"):
		return "storebot"
	case strings.Contains(ua, "Google-InspectionTool"):
		return "inspection"
	case strings.Contains(ua, "Googlebot"):
		if strings.Contains(ua, "Android") || strings.Contains(ua, "Mobile") {
			return "smartphone"
		}
		return "desktop"
	}
	return "other"
}

// scode is the counter columns a status adds to.
func scode(st string) []string {
	var out []string
	switch st[:1] {
	case "2":
		out = append(out, "s2xx")
	case "3":
		out = append(out, "s3xx")
	case "4":
		out = append(out, "s4xx")
	case "5":
		out = append(out, "s5xx")
	}
	if c := "s" + st; statusSet[c] && !(len(out) > 0 && out[0] == c) {
		out = append(out, c)
	}
	return out
}

// unjson turns an nginx escape=json string body into text. Non-UTF-8 bytes arrive as \u00XX each: when every
// character is below U+0100 and one is above U+007F, the bytes are rebuilt and read as UTF-8.
func unjson(s string) string {
	var t string
	if json.Unmarshal([]byte(`"`+s+`"`), &t) != nil {
		return s
	}
	hi, ascii := false, true
	for _, r := range t {
		if r >= 0x100 {
			hi = true
			break
		}
		if r > 127 {
			ascii = false
		}
	}
	if t != "" && !hi && !ascii {
		b := make([]byte, 0, len(t))
		for _, r := range t {
			b = append(b, byte(r))
		}
		if utf8.Valid(b) {
			return string(b)
		}
	}
	return t
}

// unquote is Python's urllib.parse.unquote(s, errors="replace"): runs of %XX become bytes read as UTF-8 (a bad byte is
// U+FFFD), a broken escape stays as it is.
func unquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var out strings.Builder
	var run []byte
	flush := func() {
		for len(run) > 0 {
			r, n := utf8.DecodeRune(run)
			out.WriteRune(r)
			run = run[n:]
		}
	}
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			run = append(run, byte(v))
			i += 3
			continue
		}
		flush()
		out.WriteByte(s[i])
		i++
	}
	flush()
	return out.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

var ctrlRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func runeCut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// parseQSL is Python's parse_qsl(q, keep_blank_values=True).
func parseQSL(q string) [][2]string {
	var out [][2]string
	for _, nv := range strings.Split(q, "&") {
		if nv == "" {
			continue
		}
		k, v, _ := strings.Cut(nv, "=")
		out = append(out, [2]string{unquote(strings.ReplaceAll(k, "+", " ")), unquote(strings.ReplaceAll(v, "+", " "))})
	}
	return out
}

// Normalize is (normalised path?query, hash of the raw query or ""): the fragment dropped, the path percent-decoded
// (broken escapes kept, control characters replaced), the query parameters sorted and deduplicated with their value
// kept only for page; cut to 300 characters.
func Normalize(uri string) (string, string) {
	uri, _, _ = strings.Cut(uri, "#")
	path, q, _ := strings.Cut(uri, "?")
	path = unquote(path)
	if path == "" {
		path = "/"
	}
	path = ctrlRe.ReplaceAllString(path, "?")
	if q == "" {
		return runeCut(path, urlMax), ""
	}
	pairs := parseQSL(q)
	seen := map[[2]string]bool{}
	var uniq [][2]string
	for _, p := range pairs {
		if !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	sort.Slice(uniq, func(i, j int) bool {
		if uniq[i][0] != uniq[j][0] {
			return uniq[i][0] < uniq[j][0]
		}
		return uniq[i][1] < uniq[j][1]
	})
	var keep []string
	have := map[string]bool{}
	for _, p := range uniq {
		k := p[0]
		if queryAllow[k] {
			k = p[0] + "=" + p[1]
		}
		if !have[k] {
			have[k] = true
			keep = append(keep, k)
		}
	}
	norm := path
	if len(keep) > 0 {
		norm = path + "?" + strings.Join(keep, "&")
	}
	sum := sha1.Sum([]byte(q))
	return runeCut(norm, urlMax), hex.EncodeToString(sum[:])[:12]
}

// Class is one path class: the first matching regex wins.
type Class struct {
	Name string
	Re   *regexp.Regexp
}

// LoadClasses reads a path class file (class<TAB>regex lines, # comments).
func LoadClasses(r io.Reader) ([]Class, error) {
	var out []Class
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rx, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("line %d: want class<TAB>regex", n)
		}
		re, err := regexp.Compile(rx)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", n, err)
		}
		out = append(out, Class{name, re})
	}
	return out, sc.Err()
}

// LoadClassesFile reads the path class file, or the built-in default when path is "" or missing.
func LoadClassesFile(path string) ([]Class, error) {
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return LoadClasses(bytes.NewReader(b))
		}
	}
	return LoadClasses(strings.NewReader(DefaultClasses))
}

// DefaultClasses are the path classes when no file is configured: what every site has.
const DefaultClasses = "robots\t^/robots\\.txt$\n" +
	"sitemap\t^/sitemap[^/]*\\.xml(\\.gz)?$\n" +
	"api\t^/(api|_next/data)(/|$)\n" +
	"jscss\t^/(_next/static|static|assets)/|\\.(js|css|map|woff2?|ttf|eot)$\n" +
	"image\t\\.(jpe?g|png|gif|webp|avif|svg|ico)$|^/(image|images|media|uploads)/\n" +
	"blog\t^/blog(/|$)\n" +
	"home\t^/$\n"

// PClass is the class of a normalised URL (its path only); "other" when none matches.
func PClass(u string, classes []Class) string {
	p, _, _ := strings.Cut(u, "?")
	for _, c := range classes {
		if c.Re.MatchString(p) {
			return c.Name
		}
	}
	return "other"
}

// upKind is the kind of upstream that answered: cache (a cache edge group), edge (the edge answered itself: return,
// redirect, limit), origin (a private 10.x address), local (127.x, the edge's own blocks), external.
func upKind(up string, cacheGroup bool) string {
	switch {
	case cacheGroup:
		return "cache"
	case up == "" || up == "-":
		return "edge"
	case strings.HasPrefix(up, "10."):
		return "origin"
	case strings.HasPrefix(up, "127."):
		return "local"
	}
	return "external"
}

// Nets are Google's crawler address ranges.
type Nets []*net.IPNet

// Contains says whether ip (text) is in one of the ranges.
func (ns Nets) Contains(ip string) bool {
	a := net.ParseIP(ip)
	if a == nil {
		return false
	}
	for _, n := range ns {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// whoIs: real = an address in Google's crawler ranges; internal = loopback or private (the edge's own subrequests,
// whose outer request is already counted); fake = anything else with a Google user agent.
func whoIs(ip string, ns Nets) string {
	if ns.Contains(ip) {
		return "real"
	}
	if strings.HasPrefix(ip, "127.") || strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "192.168.") || ip == "::1" {
		return "internal"
	}
	return "fake"
}

// errPrefix is the first path segment of a raw request URI ("/product-1/x?y" -> "/product-1").
func errPrefix(uri string) string {
	if !strings.HasPrefix(uri, "/") {
		return ""
	}
	end := strings.IndexAny(uri[1:], "/?\"")
	if end < 0 {
		return runeCut(uri, 60)
	}
	return runeCut(uri[:end+1], 60)
}
