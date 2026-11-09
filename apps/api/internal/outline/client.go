// Package outline is the documentation publisher (the predecessor's outline_sync.py and outline_client.py,
// ported): the Settings -> Integrations -> Outline requests (connect, settings, sync, hosts, disconnect) and the
// publish of the rendered service pages (ops.db outline_pages) under a configured parent document. It never
// deletes, moves, archives or shares a document, never publishes at the collection root, and keeps every word a
// person wrote outside Argus's block.
package outline

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Methods are the only API methods ever called.
var Methods = []string{"auth.info", "collections.list", "collections.create", "documents.create", "documents.update",
	"documents.info", "documents.search", "documents.list"}

// Error is a failed call; Status is the HTTP status (0 for a network error).
type Error struct {
	Msg    string
	Status int
	Auth   bool // 401/403: a bad token is never retried
	Budget bool // the run's call budget is spent
}

func (e *Error) Error() string { return e.Msg }

func asErr(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// Client is an Outline API client: POST + JSON + Bearer, connected to the address the SSRF guard checked (Pin)
// while TLS verifies the URL's host name; redirects are refused.
type Client struct {
	Base, token string
	Pin         string // the checked address ("" = resolve normally)
	Budget      int
	MinGap      time.Duration
	Retries     int
	MaxWait     time.Duration
	Sleep       func(time.Duration)
	AllowHTTP   bool // tests only
	Calls       int

	prefix string
	http   *http.Client
	last   time.Time
}

// New checks the URL and the token and builds a client.
func New(base, token, pin string, allowHTTP bool) (*Client, error) {
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
		return nil, &Error{Msg: "the Outline URL must be https://"}
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, &Error{Msg: "the Outline URL must be a plain https://host[/path] URL"}
	}
	if token == "" || strings.ContainsAny(token, "\r\n \t") {
		return nil, &Error{Msg: "no usable token", Auth: true}
	}
	c := &Client{Base: base, token: token, Pin: pin, Budget: 400, MinGap: 400 * time.Millisecond, Retries: 3,
		MaxWait: 60 * time.Second, AllowHTTP: allowHTTP, prefix: strings.TrimRight(u.Path, "/")}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if c.Pin != "" {
				addr = net.JoinHostPort(c.Pin, port)
			}
			return dialer.DialContext(ctx, network, addr)
		}}
	c.http = &http.Client{Transport: tr, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c, nil
}

func (c *Client) scrub(s string) string {
	if c.token != "" {
		s = strings.ReplaceAll(s, c.token, "[REDACTED]")
	}
	return s
}

func (c *Client) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
	} else {
		time.Sleep(d)
	}
}

func reason(body []byte) string {
	var d map[string]any
	if json.Unmarshal(body, &d) != nil {
		if len(body) > 120 {
			body = body[:120]
		}
		return strings.TrimSpace(string(body))
	}
	var parts []string
	for _, k := range []string{"error", "message"} {
		if v, ok := d[k]; ok && v != nil && fmt.Sprint(v) != "" {
			parts = append(parts, fmt.Sprint(v))
		}
	}
	s := strings.Join(parts, " - ")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func backoff(attempt int, max time.Duration) time.Duration {
	d := time.Duration(1<<attempt) * time.Second
	if d > max {
		return max
	}
	return d
}

// Call posts one API method; the answer as a map.
func (c *Client) Call(method string, body map[string]any) (map[string]any, error) {
	allowed := false
	for _, m := range Methods {
		allowed = allowed || m == method
	}
	if !allowed {
		return nil, &Error{Msg: "method not allowed: " + method}
	}
	if body == nil {
		body = map[string]any{}
	}
	data, _ := json.Marshal(body)
	attempt := 0
	for {
		if c.Calls >= c.Budget {
			return nil, &Error{Msg: fmt.Sprintf("call budget of %d used up for this run", c.Budget), Budget: true}
		}
		if gap := c.MinGap - time.Since(c.last); gap > 0 && !c.last.IsZero() {
			c.sleep(gap)
		}
		c.Calls++
		c.last = time.Now()
		req, _ := http.NewRequest(http.MethodPost, c.Base+"/api/"+method, bytes.NewReader(data))
		req.URL.Path = c.prefix + "/api/" + method
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "argus-outline-docs")
		resp, err := c.http.Do(req)
		if err != nil {
			if attempt < c.Retries {
				attempt++
				c.sleep(backoff(attempt, c.MaxWait))
				continue
			}
			msg := method + ": network error (" + err.Error() + ")"
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return nil, &Error{Msg: c.scrub(msg)}
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
		resp.Body.Close()
		st := resp.StatusCode
		if len(raw) > 16<<20 {
			return nil, &Error{Msg: method + ": response too large", Status: st}
		}
		switch {
		case st >= 200 && st < 300:
			var out map[string]any
			if json.Unmarshal(raw, &out) != nil {
				return nil, &Error{Msg: method + ": the answer is not JSON (is this Outline?)", Status: st}
			}
			return out, nil
		case st >= 300 && st < 400:
			return nil, &Error{Msg: fmt.Sprintf("%s: HTTP %d redirect refused (Argus never follows redirects)", method, st), Status: st}
		case st == 401 && strings.HasPrefix(strings.ToLower(resp.Header.Get("WWW-Authenticate")), "basic"):
			return nil, &Error{Msg: method + ": HTTP 401 with a basic-auth challenge: a proxy in front of Outline (not Outline) refused the call", Status: st, Auth: true}
		case st == 401 || st == 403:
			return nil, &Error{Msg: c.scrub(strings.TrimSpace(fmt.Sprintf("%s: HTTP %d %s", method, st, reason(raw)))), Status: st, Auth: true}
		case st == 429 && attempt < c.Retries:
			attempt++
			wait, _ := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64)
			d := time.Duration(wait * float64(time.Second))
			if b := backoff(attempt, c.MaxWait); d < b {
				d = b
			}
			if d > c.MaxWait {
				d = c.MaxWait
			}
			c.sleep(d)
			continue
		case st >= 500 && attempt < c.Retries:
			attempt++
			c.sleep(backoff(attempt, c.MaxWait))
			continue
		}
		return nil, &Error{Msg: c.scrub(strings.TrimSpace(fmt.Sprintf("%s: HTTP %d %s", method, st, reason(raw)))), Status: st}
	}
}

func dataOf(m map[string]any) map[string]any {
	d, _ := m["data"].(map[string]any)
	if d == nil {
		d = map[string]any{}
	}
	return d
}

func listOf(m map[string]any) []map[string]any {
	xs, _ := m["data"].([]any)
	var out []map[string]any
	for _, x := range xs {
		if d, ok := x.(map[string]any); ok {
			out = append(out, d)
		}
	}
	return out
}

// AuthInfo is auth.info.
func (c *Client) AuthInfo() (map[string]any, error) {
	r, err := c.Call("auth.info", nil)
	return dataOf(r), err
}

func (c *Client) paged(method string, body map[string]any, maxOffset int) ([]map[string]any, error) {
	var out []map[string]any
	for off := 0; ; off += 100 {
		b := map[string]any{"limit": 100, "offset": off}
		for k, v := range body {
			b[k] = v
		}
		r, err := c.Call(method, b)
		if err != nil {
			return out, err
		}
		page := listOf(r)
		out = append(out, page...)
		if len(page) < 100 || off > maxOffset {
			return out, nil
		}
	}
}

// Collections lists every collection.
func (c *Client) Collections() ([]map[string]any, error) {
	return c.paged("collections.list", nil, 2000)
}

// CreateCollection creates a read-only collection.
func (c *Client) CreateCollection(name, description string) (map[string]any, error) {
	r, err := c.Call("collections.create", map[string]any{"name": name, "description": description, "permission": "read"})
	return dataOf(r), err
}

// Documents lists every document of a collection.
func (c *Client) Documents(collection string) ([]map[string]any, error) {
	return c.paged("documents.list", map[string]any{"collectionId": collection}, 5000)
}

// Info is documents.info.
func (c *Client) Info(id string) (map[string]any, error) {
	r, err := c.Call("documents.info", map[string]any{"id": id})
	return dataOf(r), err
}

// Create makes a document, under parent when it is not "" (only the parent page itself is made at the root).
func (c *Client) Create(title, text, collection, parent string) (map[string]any, error) {
	b := map[string]any{"title": title, "text": text, "collectionId": collection, "publish": true}
	if parent != "" {
		b["parentDocumentId"] = parent
	}
	r, err := c.Call("documents.create", b)
	return dataOf(r), err
}

// Update replaces a document's text.
func (c *Client) Update(id, text string) (map[string]any, error) {
	r, err := c.Call("documents.update", map[string]any{"id": id, "text": text})
	return dataOf(r), err
}
