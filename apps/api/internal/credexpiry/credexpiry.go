// Package credexpiry is the credential expiry job (the worker job `cred-expiry`, daily and on "Check now" in
// Settings -> Credentials; port of the earlier installation's credential expiry job). Is any credential Argus uses
// near its expiry date?
//
// Check: each credential checks itself with one read-only call (GitLab personal_access_tokens/self, Grafana /api/user
// plus the service-account token list when the token may read it, Jira myself plus /rest/pat/latest/tokens, Matrix
// whoami, Outline auth.info) and only metadata is written to credentials.json (0644): name, kind, where it lives,
// who uses it, expiry, days left, created, last used, status, how to rotate. Never a value, a hash or a prefix of
// one; every string of the output is scrubbed for every value read.
//
// Record: credentials.json -> ops.db credential_alerts (what was raised), an incident (task "cred", key
// CRED|<id>|cred-expiry) at <= crit days or expired / rejected, one notice per band (never a ping), the incident
// resolved when the credential is OK again.
//
// Status of one credential (Classify):
//
//	absent    not configured (optional ones are not a problem)
//	rejected  the service answered 401/403 to the token itself -> expired when the last known expiry has passed
//	expired   days left <= 0                      FAIL, incident (critical), notice
//	crit      days left <= crit days (7)          WARN, incident (high), notice
//	warn      days left <= warn days (30)         WARN
//	rotate    no expiry date and older than the rotation days (180, or the credential's own) WARN
//	error     the check itself failed (network, 5xx): the last known expiry is kept, WARN
//	ok
package credexpiry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Thresholds of the classification (days).
type Thresholds struct {
	WarnDays   int `json:"warn_days"`
	CritDays   int `json:"crit_days"`
	RotateDays int `json:"rotate_days"`
	TodayDays  int `json:"today_days"`
}

// Fix keeps crit below warn, like the earlier job.
func (t Thresholds) Fix() Thresholds {
	if t.WarnDays <= 0 {
		t.WarnDays = 30
	}
	if t.CritDays < 0 {
		t.CritDays = 7
	}
	if t.RotateDays <= 0 {
		t.RotateDays = 180
	}
	if t.TodayDays < 0 {
		t.TodayDays = 14
	}
	if t.CritDays >= t.WarnDays {
		t.CritDays = min(7, t.WarnDays-1)
	}
	return t
}

// Level of each status.
var Level = map[string]string{"ok": "OK", "absent": "OK", "warn": "WARN", "rotate": "WARN", "error": "WARN", "crit": "WARN",
	"expired": "FAIL", "rejected": "FAIL"}

// AlertBands raise an incident and a notice.
var AlertBands = map[string]bool{"crit": true, "expired": true, "rejected": true}

var propose = map[string]bool{"warn": true, "rotate": true, "crit": true, "expired": true, "rejected": true}

// Cred is one credential to check. Value is read only inside the job and never written anywhere.
type Cred struct {
	ID, Name, Kind, KindLabel, UsedBy string
	Check                             string // gitlab, grafana, jira, matrix, outline, none
	URL                               string // the service address of the check
	User                              string // Jira: a user name means basic auth
	Where                             string // where it lives (a file or a setting name), never the value
	Scope                             string
	Value                             string
	CreatedAt                         int64 // when it was set (0 unknown)
	CreatedSource                     string
	Optional                          bool
	RotateDays, ValidDays             int
	Howto                             []string
}

// Item is one line of credentials.json. Pointer fields are JSON null when unknown.
type Item struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	KindLabel     string   `json:"kind_label"`
	UsedBy        string   `json:"used_by"`
	Check         string   `json:"check"`
	Scope         *string  `json:"scope"`
	Optional      bool     `json:"optional"`
	User          *string  `json:"user"`
	Identity      string   `json:"identity"`
	TokenID       *int64   `json:"token_id"`
	Scopes        []string `json:"scopes"`
	ExpiresAt     *string  `json:"expires_at"`
	ExpirySource  string   `json:"expiry_source"`
	CreatedAt     *int64   `json:"created_at"`
	CreatedSource *string  `json:"created_source"`
	LastUsed      *int64   `json:"last_used"`
	Note          string   `json:"note"`
	Where         string   `json:"where"`
	Absent        bool     `json:"absent,omitempty"`
	Rejected      bool     `json:"rejected,omitempty"`
	HTTP          any      `json:"http,omitempty"`
	Error         string   `json:"error,omitempty"`
	ValidDays     int      `json:"valid_days,omitempty"`
	RotateDays    int      `json:"rotate_days"`
	DaysLeft      *int     `json:"days_left"`
	AgeDays       *int     `json:"age_days"`
	RotateDue     *string  `json:"rotate_due"`
	RotateIn      *int     `json:"rotate_in"`
	Status        string   `json:"status"`
	Level         string   `json:"level"`
	Band          string   `json:"band"`
	Today         bool     `json:"today"`
	Detail        string   `json:"detail"`
	Howto         []string `json:"howto"`
	CheckedAt     int64    `json:"checked_at"`
}

// Next is the credential that expires next.
type Next struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	ExpiresAt *string `json:"expires_at"`
	DaysLeft  *int    `json:"days_left"`
}

// Doc is credentials.json.
type Doc struct {
	V            int               `json:"v"`
	CheckedAt    int64             `json:"checked_at"`
	Thresholds   Thresholds        `json:"thresholds"`
	Counts       map[string]int    `json:"counts"`
	Attention    int               `json:"attention"`
	Worst        string            `json:"worst"`
	Next         *Next             `json:"next"`
	Certificates map[string]string `json:"certificates"`
	Items        []*Item           `json:"items"`
}

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }

// --- dates ----------------------------------------------------------------------------------------------------------

var fracRe = regexp.MustCompile(`\.(\d{1,9})`)
var offRe = regexp.MustCompile(`([+-]\d\d)(\d\d)$`)

// ParseTime reads a date or an ISO time (GitLab "2027-05-24", Jira "2026-10-20T11:44:55.015+0000"); ok false when
// it is not one. A plain date is its midnight UTC; a time without a zone is UTC.
func ParseTime(v string) (time.Time, bool) {
	s := strings.TrimSpace(v)
	if s == "" || strings.HasPrefix(s, "0001-") {
		return time.Time{}, false
	}
	if len(s) == 10 {
		t, err := time.Parse("2006-01-02", s)
		return t, err == nil
	}
	s = strings.Replace(s, "Z", "+00:00", 1)
	s = offRe.ReplaceAllString(s, "$1:$2")
	s = fracRe.ReplaceAllString(s, "")
	for _, l := range []string{"2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05", "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func parseTS(v any) *int64 {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	t, ok := ParseTime(s)
	if !ok {
		return nil
	}
	u := t.Unix()
	return &u
}

// DaysLeft is the whole days from today (UTC) to the expiry date; 0 = it expires today.
func DaysLeft(expires string, now int64) (int, bool) {
	t, ok := ParseTime(expires)
	if !ok {
		return 0, false
	}
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	n := time.Unix(now, 0).UTC()
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	return int(d.Sub(today).Hours() / 24), true
}

func neverExpires(expires string) bool {
	t, ok := ParseTime(expires)
	return ok && t.Year() >= 9000
}

func utcDate(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02") }

// --- classify -------------------------------------------------------------------------------------------------------

// Classify sets status, level, days left, band, rotation and the detail of one item.
func Classify(it *Item, th Thresholds, now int64) {
	if it.RotateDays == 0 {
		it.RotateDays = th.RotateDays
	}
	it.DaysLeft = nil
	if it.ExpiresAt != nil && neverExpires(*it.ExpiresAt) {
		it.ExpiresAt, it.ExpirySource = nil, "never"
	}
	if it.ExpiresAt != nil {
		if d, ok := DaysLeft(*it.ExpiresAt, now); ok {
			it.DaysLeft = ip(d)
		}
	}
	it.AgeDays, it.RotateDue, it.RotateIn = nil, nil, nil
	if it.CreatedAt != nil && *it.CreatedAt > 0 {
		it.AgeDays = ip(int((now - *it.CreatedAt) / 86400))
	}
	if it.DaysLeft == nil && it.AgeDays != nil {
		it.RotateIn = ip(it.RotateDays - *it.AgeDays)
		it.RotateDue = sp(utcDate(*it.CreatedAt + int64(it.RotateDays)*86400))
	}
	d := it.DaysLeft
	var st string
	switch {
	case it.Absent:
		st = "absent"
	case it.Rejected:
		st = "rejected"
		if d != nil && *d <= 0 {
			st = "expired"
		}
	case d != nil && *d <= 0:
		st = "expired"
	case d != nil && *d <= th.CritDays:
		st = "crit"
	case d != nil && *d <= th.WarnDays:
		st = "warn"
	case it.Error != "":
		st = "error"
	case it.RotateIn != nil && *it.RotateIn <= 0:
		st = "rotate"
	default:
		st = "ok"
	}
	it.Status, it.Level = st, Level[st]
	switch {
	case st == "absent":
		it.Band = "absent"
	case d == nil:
		it.Band = "unknown"
		if it.ExpirySource == "never" {
			it.Band = "never"
		}
	case *d <= 0:
		it.Band = "expired"
	case *d <= th.CritDays:
		it.Band = "crit"
	case *d <= th.WarnDays:
		it.Band = "warn"
	default:
		it.Band = "ok"
	}
	it.Today = st == "expired" || st == "rejected" || (d != nil && *d <= th.TodayDays && st != "absent")
	it.Detail = detailOf(it)
}

func detailOf(it *Item) string {
	st, d := it.Status, it.DaysLeft
	if st == "absent" {
		return "not configured"
	}
	if st == "rejected" {
		h := "auth error"
		if it.HTTP != nil && fmt.Sprint(it.HTTP) != "" {
			h = fmt.Sprint(it.HTTP)
		}
		return "rejected by the service (" + h + "): revoked, expired or wrong"
	}
	var bits []string
	if d != nil {
		src := map[string]string{"estimate": "estimated", "last-known": "last known"}[it.ExpirySource]
		if *d <= 0 {
			b := "expired " + *it.ExpiresAt
			if src != "" {
				b += " (" + src + ")"
			}
			bits = append(bits, b)
		} else {
			extra := ""
			if src != "" {
				extra = ", " + src
			}
			bits = append(bits, fmt.Sprintf("expires %s (%d d%s)", *it.ExpiresAt, *d, extra))
		}
	} else if it.ExpirySource == "never" {
		bits = append(bits, "never expires")
	} else {
		bits = append(bits, "no expiry info")
	}
	if d == nil && it.RotateDue != nil {
		b := fmt.Sprintf("set %d d ago, rotate by %s", *it.AgeDays, *it.RotateDue)
		if it.RotateIn == nil || *it.RotateIn <= 0 {
			b += " (overdue)"
		}
		bits = append(bits, b)
	}
	if it.Error != "" {
		bits = append(bits, "check failed: "+it.Error)
	}
	return strings.Join(bits, "; ")
}

// --- probes (read-only calls; the token goes only into the request header) --------------------------------------------

// Prober makes the read-only calls.
type Prober struct {
	Client    *http.Client
	AllowHTTP bool // tests and test installations with fake services only
}

const maxBody = 1 << 20

var errRefused = errors.New("refused")

func (p *Prober) call(ctx context.Context, method, base, path string, hdr map[string]string, body []byte) (int, any, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + path)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: bad URL", errRefused)
	}
	if u.Scheme != "https" && !(p.AllowHTTP && u.Scheme == "http") {
		return 0, nil, fmt.Errorf("%w: https only", errRefused)
	}
	if u.Hostname() == "" || u.User != nil {
		return 0, nil, fmt.Errorf("%w: bad URL", errRefused)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "argus-cred-expiry")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := p.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	var data any
	if len(raw) > 0 {
		if json.Unmarshal(raw, &data) != nil {
			data = nil
		}
	}
	return resp.StatusCode, data, nil
}

func errText(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, errRefused):
		return "refused: " + strings.TrimPrefix(err.Error(), "refused: ")
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "x509:"):
		return "TLS error"
	}
	return "connection error"
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func intOf(v any) *int64 {
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		i := int64(f)
		return &i
	}
	return nil
}

func (p *Prober) rejected(it *Item, st int) bool {
	if st == 401 || st == 403 {
		it.Rejected, it.HTTP = true, st
		return true
	}
	return false
}

func (p *Prober) gitlab(ctx context.Context, it *Item, c Cred) error {
	st, d, err := p.call(ctx, "GET", c.URL, "/api/v4/personal_access_tokens/self", map[string]string{"PRIVATE-TOKEN": c.Value}, nil)
	if err != nil || p.rejected(it, st) {
		return err
	}
	m, ok := d.(map[string]any)
	if st != 200 || !ok {
		it.Error = fmt.Sprintf("HTTP %d", st)
		return nil
	}
	it.Identity = clip(str(m["name"]), 80)
	it.TokenID = intOf(m["id"])
	it.Scopes = nil
	if sc, ok := m["scopes"].([]any); ok {
		for _, s := range sc {
			if x, ok := s.(string); ok && len(it.Scopes) < 10 {
				it.Scopes = append(it.Scopes, x)
			}
		}
	}
	it.ExpiresAt, it.ExpirySource = nil, "none"
	if e, ok := m["expires_at"].(string); ok {
		it.ExpiresAt, it.ExpirySource = &e, "api"
	}
	if ct := parseTS(m["created_at"]); ct != nil {
		it.CreatedAt, it.CreatedSource = ct, sp("api")
	}
	it.LastUsed = parseTS(m["last_used_at"])
	if m["revoked"] == true || m["active"] == false {
		it.Rejected, it.HTTP = true, "inactive"
	}
	return nil
}

func (p *Prober) grafana(ctx context.Context, it *Item, c Cred) error {
	hdr := map[string]string{"Authorization": "Bearer " + c.Value}
	st, d, err := p.call(ctx, "GET", c.URL, "/api/user", hdr, nil)
	if err != nil || p.rejected(it, st) {
		return err
	}
	m, ok := d.(map[string]any)
	if st != 200 || !ok {
		it.Error = fmt.Sprintf("HTTP %d", st)
		return nil
	}
	login := clip(str(m["login"]), 80)
	it.Identity, it.ExpirySource = login, "none"
	it.Note = "Grafana does not let this token read its own expiry"
	st, s, err := p.call(ctx, "GET", c.URL, "/api/serviceaccounts/search?perpage=20&query="+url.QueryEscape(login), hdr, nil)
	sm, ok := s.(map[string]any)
	if err != nil || st != 200 || !ok {
		return nil
	}
	var id *int64
	accts, _ := sm["serviceAccounts"].([]any)
	for _, a := range accts {
		if am, ok := a.(map[string]any); ok && str(am["login"]) == login {
			id = intOf(am["id"])
			break
		}
	}
	if id == nil {
		return nil
	}
	st, t, err := p.call(ctx, "GET", c.URL, fmt.Sprintf("/api/serviceaccounts/%d/tokens", *id), hdr, nil)
	toks, ok := t.([]any)
	if err != nil || st != 200 || !ok || len(toks) == 0 {
		return nil
	}
	if len(toks) > 1 {
		it.Note = fmt.Sprintf("%d tokens on the service account: cannot tell which one this is", len(toks))
		return nil
	}
	tk, _ := toks[0].(map[string]any)
	it.Note = ""
	it.TokenID = intOf(tk["id"])
	exp := str(tk["expiration"])
	if exp != "" && !strings.HasPrefix(exp, "0001") {
		it.ExpiresAt, it.ExpirySource = sp(clip(exp, 10)), "api"
	} else {
		it.ExpiresAt, it.ExpirySource = nil, "never"
	}
	if ct := parseTS(tk["created"]); ct != nil {
		it.CreatedAt, it.CreatedSource = ct, sp("api")
	}
	it.LastUsed = parseTS(tk["lastUsedAt"])
	return nil
}

func (p *Prober) jira(ctx context.Context, it *Item, c Cred) error {
	hdr := map[string]string{"Authorization": "Bearer " + c.Value}
	if c.User != "" {
		hdr["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(c.User+":"+c.Value))
	}
	st, d, err := p.call(ctx, "GET", c.URL, "/rest/api/2/myself", hdr, nil)
	if err != nil || p.rejected(it, st) {
		return err
	}
	m, ok := d.(map[string]any)
	if st != 200 || !ok {
		it.Error = fmt.Sprintf("HTTP %d", st)
		return nil
	}
	who := str(m["name"])
	if who == "" {
		who = str(m["displayName"])
	}
	it.Identity, it.ExpirySource = clip(who, 80), "none"
	st, t, err := p.call(ctx, "GET", c.URL, "/rest/pat/latest/tokens", hdr, nil)
	arr, ok := t.([]any)
	if err != nil || st != 200 || !ok {
		return nil
	}
	var best map[string]any
	var bestTS int64 = -1
	for _, x := range arr {
		if xm, ok := x.(map[string]any); ok {
			ts := int64(0)
			if v := parseTS(xm["lastAccessedAt"]); v != nil {
				ts = *v
			}
			if ts > bestTS {
				best, bestTS = xm, ts
			}
		}
	}
	if best == nil {
		return nil
	}
	if it.Identity != "" {
		it.Identity += " · "
	}
	it.Identity += clip(str(best["name"]), 60)
	it.TokenID = intOf(best["id"])
	it.ExpiresAt, it.ExpirySource = nil, "none"
	if e := str(best["expiringAt"]); e != "" {
		if tt, ok := ParseTime(e); ok {
			e = tt.Format("2006-01-02")
		}
		it.ExpiresAt, it.ExpirySource = &e, "api"
	}
	if ct := parseTS(best["createdAt"]); ct != nil {
		it.CreatedAt, it.CreatedSource = ct, sp("api")
	}
	it.LastUsed = parseTS(best["lastAccessedAt"])
	return nil
}

func (p *Prober) matrix(ctx context.Context, it *Item, c Cred) error {
	st, d, err := p.call(ctx, "GET", c.URL, "/_matrix/client/v3/account/whoami", map[string]string{"Authorization": "Bearer " + c.Value}, nil)
	if err != nil || p.rejected(it, st) {
		return err
	}
	m, ok := d.(map[string]any)
	if st != 200 || !ok {
		it.Error = fmt.Sprintf("HTTP %d", st)
		return nil
	}
	it.Identity, it.ExpirySource = clip(str(m["user_id"]), 80), "none"
	return nil
}

func (p *Prober) outline(ctx context.Context, it *Item, c Cred) error {
	st, d, err := p.call(ctx, "POST", c.URL, "/api/auth.info", map[string]string{"Authorization": "Bearer " + c.Value}, []byte("{}"))
	if err != nil || p.rejected(it, st) {
		return err
	}
	m, ok := d.(map[string]any)
	if st != 200 || !ok {
		it.Error = fmt.Sprintf("HTTP %d", st)
		return nil
	}
	it.Identity = ""
	if dm, ok := m["data"].(map[string]any); ok {
		if u, ok := dm["user"].(map[string]any); ok {
			it.Identity = clip(str(u["name"]), 80)
		}
	}
	it.ExpirySource = "none"
	return nil
}

// Probe runs the check of one credential's kind; false when the kind has no check.
func (p *Prober) Probe(ctx context.Context, it *Item, c Cred) bool {
	fns := map[string]func(context.Context, *Item, Cred) error{"gitlab": p.gitlab, "grafana": p.grafana, "jira": p.jira,
		"matrix": p.matrix, "outline": p.outline}
	fn, ok := fns[c.Check]
	if !ok {
		return false
	}
	if c.URL == "" {
		it.Error = "no service URL configured"
		return true
	}
	if err := fn(ctx, it, c); err != nil {
		it.Error = errText(err)
	}
	return true
}

// --- check ----------------------------------------------------------------------------------------------------------

// Options of one check.
type Options struct {
	Out        string // credentials.json
	Thresholds Thresholds
	Prober     *Prober
	Now        func() time.Time
}

func base(c Cred) *Item {
	it := &Item{ID: c.ID, Name: c.Name, Kind: c.Kind, KindLabel: c.KindLabel, UsedBy: c.UsedBy, Check: c.Check, Optional: c.Optional,
		ExpirySource: "none", Scopes: []string{}, Where: c.Where, Howto: c.Howto, RotateDays: c.RotateDays}
	if it.Check == "" {
		it.Check = "none"
	}
	if it.KindLabel == "" {
		it.KindLabel = it.Kind
	}
	if c.Scope != "" {
		it.Scope = sp(c.Scope)
	}
	if it.Howto == nil {
		it.Howto = []string{}
	}
	return it
}

// CheckOne checks one credential; prev is its last result (for the last known expiry).
func (o *Options) CheckOne(ctx context.Context, c Cred, prev *Item, now int64) *Item {
	it := base(c)
	if strings.TrimSpace(c.Value) == "" {
		it.Absent = true
		Classify(it, o.Thresholds, now)
		return it
	}
	if c.CreatedAt > 0 {
		it.CreatedAt, it.CreatedSource = &c.CreatedAt, sp(c.CreatedSource)
	}
	if o.Prober != nil {
		o.Prober.Probe(ctx, it, c)
	}
	if c.ValidDays > 0 && it.ExpiresAt == nil && it.CreatedAt != nil {
		it.ExpiresAt = sp(utcDate(*it.CreatedAt + int64(c.ValidDays)*86400))
		it.ExpirySource, it.ValidDays = "estimate", c.ValidDays
	}
	if prev != nil && (it.Rejected || it.Error != "") && it.ExpiresAt == nil && prev.ExpiresAt != nil {
		it.ExpiresAt, it.ExpirySource = prev.ExpiresAt, "last-known"
		if it.TokenID == nil {
			it.TokenID = prev.TokenID
		}
		if it.Identity == "" {
			it.Identity = prev.Identity
		}
	}
	Classify(it, o.Thresholds, now)
	return it
}

var order = map[string]int{"expired": 0, "rejected": 0, "crit": 1, "warn": 2, "rotate": 3, "error": 4, "ok": 5, "absent": 6}

// Check checks every credential and writes credentials.json (metadata only, scrubbed).
func (o *Options) Check(ctx context.Context, creds []Cred) (*Doc, error) {
	now := time.Now().Unix()
	if o.Now != nil {
		now = o.Now().Unix()
	}
	th := o.Thresholds.Fix()
	o.Thresholds = th
	prev := map[string]*Item{}
	if old, err := Load(o.Out); err == nil {
		for _, it := range old.Items {
			prev[it.ID] = it
		}
	}
	var secrets []string
	items := []*Item{}
	seen := map[string]bool{}
	for _, c := range creds {
		if !idRe.MatchString(c.ID) || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		if c.Value != "" {
			secrets = append(secrets, c.Value)
		}
		it := o.CheckOne(ctx, c, prev[c.ID], now)
		it.CheckedAt = now
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if order[a.Status] != order[b.Status] {
			return order[a.Status] < order[b.Status]
		}
		da, db := 1<<30, 1<<30
		if a.DaysLeft != nil {
			da = *a.DaysLeft
		}
		if b.DaysLeft != nil {
			db = *b.DaysLeft
		}
		if da != db {
			return da < db
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	doc := &Doc{V: 1, CheckedAt: now, Thresholds: th, Counts: map[string]int{}, Worst: "OK", Items: items,
		Certificates: map[string]string{"note": "TLS certificates are checked by the certificates job; see Certificates.", "link": "/certs"}}
	for _, it := range items {
		doc.Counts[it.Status]++
		if propose[it.Status] {
			doc.Attention++
		}
		if it.DaysLeft != nil && *it.DaysLeft > 0 && it.Status != "absent" && (doc.Next == nil || *it.DaysLeft < *doc.Next.DaysLeft) {
			doc.Next = &Next{ID: it.ID, Name: it.Name, ExpiresAt: it.ExpiresAt, DaysLeft: it.DaysLeft}
		}
	}
	if len(items) > 0 {
		doc.Worst = Level[items[0].Status]
	}
	b, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return nil, err
	}
	b = Scrub(b, secrets)
	if err := writeFile(o.Out, append(b, '\n')); err != nil {
		return nil, err
	}
	var out Doc
	json.Unmarshal(b, &out)
	return &out, nil
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,60}$`)

// Scrub replaces every secret value (6 characters or more) in the JSON text, longest first, also in its JSON-escaped
// form: nothing read can leave by accident.
func Scrub(b []byte, secrets []string) []byte {
	var vals []string
	for _, s := range secrets {
		if len(s) >= 6 {
			vals = append(vals, s)
		}
	}
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, s := range vals {
		esc, _ := json.Marshal(s)
		b = bytes.ReplaceAll(b, esc[1:len(esc)-1], []byte("[redacted]"))
		b = bytes.ReplaceAll(b, []byte(s), []byte("[redacted]"))
	}
	return b
}

// Load reads credentials.json.
func Load(path string) (*Doc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), ".credentials.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	os.Chmod(tmp, 0o644)
	return os.Rename(tmp, path)
}

// Summary is the job's printed result (names only).
func Summary(d *Doc) []string {
	out := []string{fmt.Sprintf("credentials: %d checked, %d need attention", len(d.Items), d.Attention)}
	for _, it := range d.Items {
		out = append(out, fmt.Sprintf("  %-8s %s: %s", it.Status, it.Name, it.Detail))
	}
	return out
}
