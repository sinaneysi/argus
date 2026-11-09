// Package google is the read-only client of the marketing module for Google's search APIs: a service-account key
// (a Settings -> Data sources credential of type google, sealed with the master key) signs a JWT (RS256), which is
// traded for an access token with a read-only scope, then Search Console (search analytics, URL inspection, sitemaps)
// and, when a GA4 property is set, the Analytics Data API are read.
//
// Network: Google is reached directly, or through the standard HTTPS_PROXY / NO_PROXY of the worker's environment
// when the installation has no direct way out. No tunnel or host job is needed.
package google

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Scopes.
const (
	ScopeSearchConsole = "https://www.googleapis.com/auth/webmasters.readonly"
	ScopeAnalytics     = "https://www.googleapis.com/auth/analytics.readonly"
	// DefaultBase is the address of a data source that talks to Google itself.
	DefaultBase = "https://www.googleapis.com"
)

// Key is a service-account key file's fields that are used (the private key never leaves this package).
type Key struct {
	Email    string `json:"client_email"`
	TokenURI string `json:"token_uri"`
	pk       *rsa.PrivateKey
}

// ParseKey reads a service-account key (the JSON file Google gives, on one line or many).
func ParseKey(raw string) (*Key, error) {
	var k struct {
		Type       string `json:"type"`
		Email      string `json:"client_email"`
		TokenURI   string `json:"token_uri"`
		PrivateKey string `json:"private_key"`
	}
	if json.Unmarshal([]byte(raw), &k) != nil {
		return nil, errors.New("the key is not a service-account JSON key")
	}
	if k.Email == "" || k.PrivateKey == "" {
		return nil, errors.New("the key has no client_email or private_key")
	}
	if k.TokenURI == "" {
		k.TokenURI = "https://oauth2.googleapis.com/token"
	}
	blk, _ := pem.Decode([]byte(k.PrivateKey))
	if blk == nil {
		return nil, errors.New("the key's private_key is not PEM")
	}
	var pk *rsa.PrivateKey
	if p, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
		pk, _ = p.(*rsa.PrivateKey)
	} else if p, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		pk = p
	}
	if pk == nil {
		return nil, errors.New("the key's private_key is not an RSA key")
	}
	return &Key{Email: k.Email, TokenURI: k.TokenURI, pk: pk}, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// assertion is the signed JWT for one scope.
func (k *Key) assertion(scope string, now time.Time) (string, error) {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	c, _ := json.Marshal(map[string]any{"iss": k.Email, "scope": scope, "aud": k.TokenURI, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	msg := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.pk, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return msg + "." + b64(sig), nil
}

// Client reads Google's APIs with one key.
type Client struct {
	Key  *Key
	Base string // "" or DefaultBase: Google's own hosts; anything else (a test service): every API under it
	HTTP *http.Client
	// AllowHTTP lets http:// addresses through (test installations with fake services only)
	AllowHTTP bool
	Wait      []time.Duration // between attempts (default 5 s, 15 s, 30 s)
	Now       func() time.Time

	tokens map[string]string
}

// HTTPClient is the default client: the environment's proxy, 60 s per call.
func HTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 15 * time.Second}}
}

func (c *Client) hc() *http.Client {
	if c.HTTP == nil {
		c.HTTP = HTTPClient()
	}
	return c.HTTP
}

func (c *Client) api(host, path string) string {
	if c.Base == "" || c.Base == DefaultBase {
		return "https://" + host + path
	}
	return strings.TrimRight(c.Base, "/") + path
}

func (c *Client) checkURL(u string) error {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || !(p.Scheme == "https" || c.AllowHTTP && p.Scheme == "http") {
		return fmt.Errorf("refused address (https only)")
	}
	return nil
}

// HTTPError is an answer with a status that is not 200.
type HTTPError struct {
	Code int
	Path string
	Body string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("%d %s: %s", e.Code, e.Path, e.Body) }

// do is one call with retries on 429, 5xx and network errors.
func (c *Client) do(ctx context.Context, method, u string, body []byte, form bool, tok string, out any) error {
	if err := c.checkURL(u); err != nil {
		return err
	}
	wait := c.Wait
	if wait == nil {
		wait = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}
	}
	path := u
	if i := strings.Index(u, "?"); i >= 0 {
		path = u[:i]
	}
	var last error
	for attempt := 0; attempt <= len(wait); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait[attempt-1]):
			}
		}
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return err
		}
		if form {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := c.hc().Do(req)
		if err != nil {
			last = errors.New("network error: " + netWords(err))
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if rerr != nil {
			last = errors.New("the answer was cut off")
			continue
		}
		if resp.StatusCode != 200 {
			last = &HTTPError{resp.StatusCode, path, strings.TrimSpace(string(b[:min(len(b), 200)]))}
			if resp.StatusCode < 500 && resp.StatusCode != 429 {
				return last
			}
			continue
		}
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("%s: the answer is not JSON", path)
		}
		return nil
	}
	return last
}

func netWords(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "no answer in time"
	case strings.Contains(err.Error(), "proxy"):
		return "the proxy failed"
	case strings.Contains(err.Error(), "no such host"):
		return "the host name does not resolve"
	case strings.Contains(err.Error(), "connection refused"):
		return "connection refused"
	}
	return "no connection"
}

// Token is an access token for scope (cached per client).
func (c *Client) Token(ctx context.Context, scope string) (string, error) {
	if t := c.tokens[scope]; t != "" {
		return t, nil
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	a, err := c.Key.assertion(scope, now)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {a}}.Encode()
	var r struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.do(ctx, "POST", c.Key.TokenURI, []byte(form), true, "", &r); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && (he.Code == 400 || he.Code == 401) {
			return "", errors.New("Google refused the key (" + he.Path + " answered " + fmt.Sprint(he.Code) + ")")
		}
		return "", err
	}
	if r.AccessToken == "" {
		return "", errors.New("the token answer has no access_token")
	}
	if c.tokens == nil {
		c.tokens = map[string]string{}
	}
	c.tokens[scope] = r.AccessToken
	return r.AccessToken, nil
}

func (c *Client) call(ctx context.Context, scope, method, u string, body any, out any) error {
	tok, err := c.Token(ctx, scope)
	if err != nil {
		return err
	}
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	return c.do(ctx, method, u, b, false, tok, out)
}

func site(s string) string { return url.PathEscape(s) }

// Row is one Search Analytics row.
type Row struct {
	Keys        []string `json:"keys"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	CTR         float64  `json:"ctr"`
	Position    float64  `json:"position"`
}

// SearchAnalytics is the query's rows, paged (25,000 a page) up to max rows.
func (c *Client) SearchAnalytics(ctx context.Context, property, start, end string, dims []string, max int) ([]Row, error) {
	u := c.api("www.googleapis.com", "/webmasters/v3/sites/"+site(property)+"/searchAnalytics/query")
	var out []Row
	const page = 25000
	for off := 0; off < max; off += page {
		body := map[string]any{"startDate": start, "endDate": end, "dimensions": dims}
		if len(dims) > 1 || dims[0] != "date" {
			body["rowLimit"], body["startRow"] = page, off
		}
		var r struct {
			Rows []Row `json:"rows"`
		}
		if err := c.call(ctx, ScopeSearchConsole, "POST", u, body, &r); err != nil {
			return out, err
		}
		out = append(out, r.Rows...)
		if len(r.Rows) < page {
			break
		}
	}
	return out, nil
}

// Inspection is the index state of one URL.
type Inspection struct {
	Verdict    string `json:"verdict"`
	Coverage   string `json:"coverageState"`
	LastCrawl  string `json:"lastCrawlTime"`
	CrawledAs  string `json:"crawledAs"`
	Robots     string `json:"robotsTxtState"`
	FetchState string `json:"pageFetchState"`
}

// Inspect is the URL Inspection of one URL.
func (c *Client) Inspect(ctx context.Context, property, u string) (Inspection, error) {
	var r struct {
		Result struct {
			Index Inspection `json:"indexStatusResult"`
		} `json:"inspectionResult"`
	}
	err := c.call(ctx, ScopeSearchConsole, "POST", c.api("searchconsole.googleapis.com", "/v1/urlInspection/index:inspect"),
		map[string]string{"inspectionUrl": u, "siteUrl": property}, &r)
	return r.Result.Index, err
}

// Sitemap is one submitted sitemap's state.
type Sitemap struct {
	Path           string `json:"path"`
	LastDownloaded string `json:"lastDownloaded"`
	Pending        bool   `json:"isPending"`
	Errors         any    `json:"errors"`
	Warnings       any    `json:"warnings"`
	Contents       []struct {
		Type      string `json:"type"`
		Submitted any    `json:"submitted"`
	} `json:"contents"`
}

// Sitemaps are the property's submitted sitemaps.
func (c *Client) Sitemaps(ctx context.Context, property string) ([]Sitemap, error) {
	var r struct {
		Sitemap []Sitemap `json:"sitemap"`
	}
	err := c.call(ctx, ScopeSearchConsole, "GET", c.api("www.googleapis.com", "/webmasters/v3/sites/"+site(property)+"/sitemaps"), nil, &r)
	return r.Sitemap, err
}

// Sites are the properties the key may read (the data source check).
func (c *Client) Sites(ctx context.Context) ([]string, error) {
	var r struct {
		SiteEntry []struct {
			SiteURL         string `json:"siteUrl"`
			PermissionLevel string `json:"permissionLevel"`
		} `json:"siteEntry"`
	}
	if err := c.call(ctx, ScopeSearchConsole, "GET", c.api("www.googleapis.com", "/webmasters/v3/sites"), nil, &r); err != nil {
		return nil, err
	}
	var out []string
	for _, s := range r.SiteEntry {
		out = append(out, s.SiteURL)
	}
	return out, nil
}

// GAPage is one GA4 organic landing page.
type GAPage struct {
	Page     string
	Sessions int64
	Bounce   float64
	Events   float64
}

// OrganicLandingPages are a GA4 property's organic landing pages of the last 28 days (500 at most, by sessions).
func (c *Client) OrganicLandingPages(ctx context.Context, property string) ([]GAPage, error) {
	body := map[string]any{"dateRanges": []map[string]string{{"startDate": "28daysAgo", "endDate": "yesterday"}},
		"dimensions": []map[string]string{{"name": "landingPage"}},
		"metrics":    []map[string]string{{"name": "sessions"}, {"name": "bounceRate"}, {"name": "keyEvents"}},
		"dimensionFilter": map[string]any{"filter": map[string]any{"fieldName": "sessionDefaultChannelGroup",
			"stringFilter": map[string]string{"value": "Organic Search"}}},
		"orderBys": []map[string]any{{"metric": map[string]string{"metricName": "sessions"}, "desc": true}}, "limit": 500}
	var r struct {
		Rows []struct {
			Dims []struct {
				Value string `json:"value"`
			} `json:"dimensionValues"`
			Mets []struct {
				Value string `json:"value"`
			} `json:"metricValues"`
		} `json:"rows"`
	}
	if err := c.call(ctx, ScopeAnalytics, "POST", c.api("analyticsdata.googleapis.com", "/v1beta/properties/"+url.PathEscape(property)+":runReport"), body, &r); err != nil {
		return nil, err
	}
	var out []GAPage
	for _, x := range r.Rows {
		if len(x.Dims) < 1 || len(x.Mets) < 3 {
			continue
		}
		var s, b, e float64
		fmt.Sscan(x.Mets[0].Value, &s)
		fmt.Sscan(x.Mets[1].Value, &b)
		fmt.Sscan(x.Mets[2].Value, &e)
		out = append(out, GAPage{x.Dims[0].Value, int64(s), b, e})
	}
	return out, nil
}

// Num is a JSON number or numeric string as an integer (Google sends int64 fields as strings).
func Num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		var n int64
		fmt.Sscan(x, &n)
		return n
	}
	return 0
}
