// Package cicd is the CI/CD watcher (the predecessor's cicd_estate.py, ported): the estate of GitLab projects with a
// CI file, the pipelines on the environment branches, failure triage by rules, the stuck-pipeline (lock) watchdog,
// deploy events, and the branch audit. It reads GitLab only (GET), through the GitLab entry of Settings -> Data
// sources with purpose agent; every fix is a proposal for a person. The pages read its rows in ARGUS_DB
// (cicd_projects, cicd_branches, cicd_pipelines, cicd_findings, cicd_deploys, cicd_state) and its status in
// ARGUS_STATE_DIR/cicd-status.json.
package cicd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/notify"
)

// ErrBudget: the call budget of this run is spent (the next run continues).
var ErrBudget = errors.New("cicd: call budget spent")

const (
	traceMax = 64 * 1024
	bodyMax  = 8 * 1024 * 1024
	fileMax  = 256 * 1024
)

// GitLab is a read-only GitLab API client with a call budget, a 429/5xx backoff and an ETag cache.
type GitLab struct {
	Base   string // the GitLab address (".../api/v4" is added)
	Token  string
	Budget int
	HTTP   *http.Client
	Sleep  func(time.Duration)

	Calls, Errors int
	cache         map[string]etagged
}

type etagged struct {
	tag  string
	data any
}

// Configured: an address and a token are set.
func (g *GitLab) Configured() bool { return g.Base != "" && g.Token != "" }

// Enc is a project path for the API (a/b/c -> a%2Fb%2Fc); ids are unchanged.
func Enc(path string) string { return url.PathEscape(path) }

func (g *GitLab) do(ctx context.Context, path string, q url.Values, raw bool, cap int) (int, []byte, http.Header, error) {
	if g.Calls >= g.Budget {
		return 0, nil, nil, fmt.Errorf("%w (%d)", ErrBudget, g.Budget)
	}
	if g.HTTP == nil {
		g.HTTP = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	if g.cache == nil {
		g.cache = map[string]etagged{}
	}
	full := strings.TrimRight(g.Base, "/") + "/api/v4/" + path
	if enc := q.Encode(); enc != "" {
		full += "?" + enc
	}
	key := path + "?" + q.Encode()
	var (
		status int
		body   []byte
		hdr    http.Header
	)
	for attempt := 0; attempt < 3; attempt++ {
		g.Calls++
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", g.Token)
		req.Header.Set("Accept", "application/json")
		cached, hasCache := g.cache[key]
		if hasCache && !raw {
			req.Header.Set("If-None-Match", cached.tag)
		}
		resp, err := g.HTTP.Do(req)
		if err != nil {
			status, hdr, body = 0, http.Header{}, nil
		} else {
			status, hdr = resp.StatusCode, resp.Header
			if raw {
				body = readTail(resp.Body, cap)
			} else {
				body, _ = io.ReadAll(io.LimitReader(resp.Body, int64(cap)))
			}
			resp.Body.Close()
		}
		if status == http.StatusNotModified && hasCache {
			b, _ := json.Marshal(cached.data)
			return 200, b, hdr, nil
		}
		if (status == 429 || status == 502 || status == 503 || status == 504 || status == 0) && attempt < 2 && g.Calls < g.Budget {
			wait := time.Duration(2*(attempt+1)) * time.Second
			if n, err := strconv.Atoi(hdr.Get("Retry-After")); err == nil {
				wait = time.Duration(min(30, n)) * time.Second
			}
			if g.Sleep != nil {
				g.Sleep(wait)
			} else {
				time.Sleep(wait)
			}
			continue
		}
		break
	}
	if (status >= 400 && status != 404) || status == 0 {
		g.Errors++
	}
	if !raw && status == 200 {
		if et := hdr.Get("ETag"); et != "" && len(g.cache) < 5000 {
			var v any
			if json.Unmarshal(body, &v) == nil {
				g.cache[key] = etagged{et, v}
			}
		}
	}
	return status, body, hdr, nil
}

func readTail(r io.Reader, cap int) []byte {
	var buf []byte
	chunk := make([]byte, 256*1024)
	total := 0
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			total += n
			buf = append(buf, chunk[:n]...)
			if len(buf) > cap {
				buf = append([]byte{}, buf[len(buf)-cap:]...)
			}
		}
		if err != nil || total > 64*1024*1024 {
			return buf
		}
	}
}

// Q builds query values from key, value pairs (a value of "" is left out).
func Q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Add(kv[i], kv[i+1])
		}
	}
	return v
}

// Get is one GET; nil when GitLab does not answer 200. Only ErrBudget and context errors are returned.
func (g *GitLab) Get(ctx context.Context, path string, q url.Values) (any, error) {
	st, body, _, err := g.do(ctx, path, q, false, bodyMax)
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, nil
	}
	var v any
	if json.Unmarshal(body, &v) != nil {
		return nil, nil
	}
	return v, nil
}

// List is Get of a list (nil when the answer is not a list).
func (g *GitLab) List(ctx context.Context, path string, q url.Values) ([]map[string]any, error) {
	v, err := g.Get(ctx, path, q)
	return maps(v), err
}

// Obj is Get of an object (an empty map when the answer is not one).
func (g *GitLab) Obj(ctx context.Context, path string, q url.Values) (map[string]any, error) {
	v, err := g.Get(ctx, path, q)
	m, _ := v.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m, err
}

// All follows the pages of a list (per_page 100 unless set), at most maxPages.
func (g *GitLab) All(ctx context.Context, path string, maxPages int, q url.Values) ([]map[string]any, error) {
	if q.Get("per_page") == "" {
		q.Set("per_page", "100")
	}
	per, _ := strconv.Atoi(q.Get("per_page"))
	var out []map[string]any
	for page := 1; page <= maxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		st, body, hdr, err := g.do(ctx, path, q, false, bodyMax)
		if err != nil {
			return out, err
		}
		var v []any
		if st != 200 || json.Unmarshal(body, &v) != nil {
			break
		}
		out = append(out, maps(v)...)
		if hdr.Get("X-Next-Page") == "" || len(v) < per {
			break
		}
	}
	return out, nil
}

// FileRaw is a repository file at ref ("" with ok false when it does not exist).
func (g *GitLab) FileRaw(ctx context.Context, project any, file, ref string) (string, bool, error) {
	st, body, _, err := g.do(ctx, fmt.Sprintf("projects/%s/repository/files/%s/raw", Enc(fmt.Sprint(project)), Enc(file)),
		Q("ref", ref), true, fileMax)
	if err != nil || st != 200 {
		return "", false, err
	}
	return string(body), true, nil
}

// TraceTail is the redacted tail of a job's log.
func (g *GitLab) TraceTail(ctx context.Context, project any, job int64) (string, error) {
	st, body, _, err := g.do(ctx, fmt.Sprintf("projects/%s/jobs/%d/trace", Enc(fmt.Sprint(project)), job), url.Values{}, true, traceMax)
	if err != nil || st != 200 {
		return "", err
	}
	r := []byte(Redact(strings.ToValidUTF8(string(body), "�")))
	if len(r) > traceMax {
		r = r[len(r)-traceMax:]
	}
	return string(r), nil
}

func maps(v any) []map[string]any {
	xs, _ := v.([]any)
	var out []map[string]any
	for _, x := range xs {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// --- redaction of job logs ---------------------------------------------------------------------------------

var (
	ansiRe    = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07`)
	sectionRe = regexp.MustCompile(`section_(?:start|end):[0-9]+:[A-Za-z0-9_.-]+(?:\[[^\]]*\])?`)
	assignRe  = regexp.MustCompile(`(?m)(^|[\s'"{,;(]|export\s)([A-Za-z_][A-Za-z0-9_]{1,80})\s*=\s*("[^"\n]*"|'[^'\n]*'|[^\s'",;)}]+)`)
	safeKeyRe = regexp.MustCompile(`^(?:ok|changed|unreachable|failed|skipped|rescued|ignored|code|status|exit_code|DEBUG|` +
		`SYSTEMD_[A-Z_]+|EXECUTOR|ENVIRONMENT|DEPLOY_TARGET|TARGET_HOSTS|MODES?|BUILD_SCRIPTS?|` +
		`NODE_VERSION|DOCKERFILE_NAME|CI_[A-Z_]+|APP_NAME|IMAGE_TAG|DEPLOY_TYPE)$`)
	safeValRe = regexp.MustCompile(`(?i)^(?:[0-9]{1,6}|true|false|null|none|yes|no|undefined|\$\{?[A-Za-z_]+\}?)$`)
	b64Re     = regexp.MustCompile(`[A-Za-z0-9+/_-]{40,}={0,2}`)
	hexRe     = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
)

func blobby(s string) bool {
	if hexRe.MatchString(s) {
		return false
	}
	if !strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || !strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") ||
		!strings.ContainsAny(s, "0123456789") {
		return false
	}
	n := 0
	for _, c := range s {
		if strings.ContainsRune("-_/.", c) {
			n++
		}
	}
	return n <= len(s)/12
}

// Redact makes untrusted log text safe to store and show: ANSI and section markers go, secrets are masked, the
// values of assignments are dropped (keys kept) except a short allow list of CI routing keys and plain values.
func Redact(text string) string {
	text = ansiRe.ReplaceAllString(text, "")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = sectionRe.ReplaceAllString(text, "")
	text, _ = notify.Redact(text, nil)
	text = assignRe.ReplaceAllStringFunc(text, func(m string) string {
		sm := assignRe.FindStringSubmatch(m)
		key, val := sm[2], sm[3]
		if safeKeyRe.MatchString(key) || safeValRe.MatchString(strings.Trim(val, `"'`)) || strings.HasPrefix(val, "[REDACTED") {
			return m
		}
		return sm[1] + key + "=<redacted>"
	})
	return b64Re.ReplaceAllStringFunc(text, func(m string) string {
		if blobby(m) {
			return "<redacted-blob>"
		}
		return m
	})
}
