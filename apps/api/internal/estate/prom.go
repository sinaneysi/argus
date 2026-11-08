// Package estate is the host estate of Argus (ADR-0021 P2): the VM inventory, the VM state and the 5-minute pulse.
// Every job here reads only: Prometheus (node_exporter series), the service map file and the capacity file. It writes
// what the inventory pages read: the ops.db tables vms, vm_changes and vm_state, and the state files
// inventory-status.json, vmstate-status.json, vm/<id>.json, vm-pulse.json and proxy-live.json.
//
// Host shell access (the earlier installation's read-only SSH snapshot and facts of every host) is not part of this
// package: it belongs to the access broker (ADR-0019, ADR-0020). Without it, services come from
// node_systemd_unit_state and the facts from node_exporter only.
//
// prom.go: the Prometheus sites. A site is one Prometheus, reached either directly or through the data-source proxy
// of a Grafana (the first Prometheus data source the token can see). Only GET requests, bounded answers, no redirects.
package estate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

// Site is one Prometheus. Name is what the inventory calls its data centre ("dc").
type Site struct {
	Name    string
	URL     string // Prometheus base URL, or the Grafana base URL when Grafana is true
	Token   string // bearer token ("" for none)
	Grafana bool

	mu  sync.Mutex
	uid string // the Grafana data source, resolved once
}

const (
	maxInstant   = 20000 // series of one instant query (the earlier job's cap)
	maxRange     = 5000  // series of one range query
	maxRangeSecs = 691200
	maxBody      = 64 << 20
)

// Client queries sites.
type Client struct {
	HTTP    *http.Client
	Timeout time.Duration
	Errors  int // failed queries since the client was made (a failure gives an empty answer, as before)
	mu      sync.Mutex
}

// NewClient is a client with TLS verification on (extra CA certificates from caFile when set) and no redirects.
func NewClient(caFile string, timeout time.Duration) (*Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no PEM certificate found", caFile)
		}
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{Timeout: timeout, HTTP: &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}, Proxy: http.ProxyFromEnvironment},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) fail() {
	c.mu.Lock()
	c.Errors++
	c.mu.Unlock()
}

func (c *Client) get(ctx context.Context, s *Site, path string, q url.Values) (any, error) {
	u := strings.TrimRight(s.URL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", s.Name, redactErr(err, s.URL))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%s: answer larger than %d bytes", s.Name, maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", s.Name, resp.StatusCode)
	}
	return py.ParseJSON(body)
}

func redactErr(err error, base string) string {
	return strings.ReplaceAll(err.Error(), base, "<site>")
}

// prefix is the path to the Prometheus API of the site.
func (c *Client) prefix(ctx context.Context, s *Site) (string, error) {
	if !s.Grafana {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uid == "" {
		v, err := c.get(ctx, s, "/api/frontend/settings", nil)
		if err != nil {
			return "", err
		}
		ds := py.AsMap(py.AsMap(v).Get("datasources"))
		if ds != nil {
			for _, k := range ds.Keys() {
				d := py.AsMap(ds.Get(k))
				if d != nil && d.S("type") == "prometheus" && d.S("uid") != "" {
					s.uid = d.S("uid")
					break
				}
			}
		}
		if s.uid == "" {
			return "", fmt.Errorf("%s: no Prometheus data source visible to this token", s.Name)
		}
	}
	return "/api/datasources/proxy/uid/" + url.PathEscape(s.uid), nil
}

// Sample is one series of an instant query: its labels and the value as text.
type Sample struct {
	Metric map[string]string
	Value  any // the value text ("1.5"); nil when absent
}

// Series is one series of a range query: its labels and its points [unix seconds, value text].
type Series struct {
	Metric map[string]string
	Values [][2]any
}

func labels(v any) map[string]string {
	out := map[string]string{}
	if m := py.AsMap(v); m != nil {
		for _, k := range m.Keys() {
			if s, ok := m.Get(k).(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

func results(v any) []any {
	m := py.AsMap(v)
	if m == nil {
		return nil
	}
	return py.AsList(py.AsMap(m.Get("data")).Get("result"))
}

// Instant is an instant query. A failure is counted and gives no series (the jobs treat it as "no data").
func (c *Client) Instant(ctx context.Context, s *Site, q string) []Sample {
	out, _ := c.InstantErr(ctx, s, q)
	return out
}

// InstantErr is Instant with the failure (counted as well).
func (c *Client) InstantErr(ctx context.Context, s *Site, q string) ([]Sample, error) {
	px, err := c.prefix(ctx, s)
	if err != nil {
		c.fail()
		return nil, err
	}
	v, err := c.get(ctx, s, px+"/api/v1/query", url.Values{"query": {q}})
	if err != nil {
		c.fail()
		return nil, err
	}
	var out []Sample
	for _, r := range results(v) {
		m := py.AsMap(r)
		if m == nil {
			continue
		}
		var val any
		if pair := m.List("value"); len(pair) == 2 {
			val = pair[1]
		}
		out = append(out, Sample{Metric: labels(m.Get("metric")), Value: val})
		if len(out) >= maxInstant {
			break
		}
	}
	return out, nil
}

// Range is a range query over the last since seconds (at most 8 days) ending at end, one point per step seconds.
func (c *Client) Range(ctx context.Context, s *Site, q string, end time.Time, since, step int64) []Series {
	if since > maxRangeSecs {
		since = maxRangeSecs
	}
	px, err := c.prefix(ctx, s)
	if err != nil {
		c.fail()
		return nil
	}
	e := end.Unix()
	v, err := c.get(ctx, s, px+"/api/v1/query_range", url.Values{"query": {q}, "start": {strconv.FormatInt(e-since, 10)},
		"end": {strconv.FormatInt(e, 10)}, "step": {strconv.FormatInt(step, 10)}})
	if err != nil {
		c.fail()
		return nil
	}
	var out []Series
	for _, r := range results(v) {
		m := py.AsMap(r)
		if m == nil {
			continue
		}
		sr := Series{Metric: labels(m.Get("metric"))}
		for _, p := range m.List("values") {
			if pair := py.AsList(p); len(pair) == 2 {
				sr.Values = append(sr.Values, [2]any{pair[0], pair[1]})
			}
		}
		out = append(out, sr)
		if len(out) >= maxRange {
			break
		}
	}
	return out
}

// ErrNoSite: no Prometheus site is configured.
var ErrNoSite = errors.New("no Prometheus site: connect Grafana, or Prometheus as an Alertmanager / Prometheus entry, in Settings -> Data sources, or set ARGUS_PROM_SOURCES")
