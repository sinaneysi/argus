package collect

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// AlertSource is one monitoring site: a name shown on /alerts (the "dc") and the URL of its alert list.
// The API follows from the path: .../api/v2/alerts is Alertmanager, .../api/v1/alerts is Prometheus.
type AlertSource struct {
	Name string
	URL  string
}

// AlertsOptions configure the alerts snapshot.
type AlertsOptions struct {
	Sources   []AlertSource
	Out       string // ARGUS_ALERTS_FILE
	TokenFile string // optional: a bearer token, read from this file, sent to every source over https or loopback
	CAFile    string // optional: extra CA certificates for the sources
	Verify    bool   // TLS certificate verification (on unless configured off)
	Timeout   time.Duration
	Now       func() time.Time // tests
}

// ParseAlertSources parses ARGUS_ALERTS_SOURCES entries "name=url". Every problem is returned.
func ParseAlertSources(list []string) ([]AlertSource, []string) {
	var out []AlertSource
	var errs []string
	seen := map[string]bool{}
	for _, e := range list {
		name, raw, ok := strings.Cut(e, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			errs = append(errs, fmt.Sprintf("%q: want name=url", e))
			continue
		}
		if seen[name] {
			errs = append(errs, fmt.Sprintf("%q: name %s used twice", e, name))
			continue
		}
		seen[name] = true
		if msg := checkAlertURL(raw); msg != "" {
			errs = append(errs, fmt.Sprintf("%s: %s", name, msg))
			continue
		}
		out = append(out, AlertSource{Name: name, URL: raw})
	}
	return out, errs
}

func checkAlertURL(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return fmt.Sprintf("not a URL: %q", raw)
	case u.Scheme != "https" && u.Scheme != "http":
		return fmt.Sprintf("must be http(s), got %q", raw)
	case u.Host == "":
		return fmt.Sprintf("has no host: %q", raw)
	case u.User != nil:
		return "must not contain a user or password (use ARGUS_ALERTS_TOKEN_FILE)"
	case apiOf(u.Path) == "":
		return fmt.Sprintf("path must end in /api/v2/alerts (Alertmanager) or /api/v1/alerts (Prometheus): %q", raw)
	}
	return ""
}

func apiOf(path string) string {
	switch {
	case strings.HasSuffix(strings.TrimRight(path, "/"), "/api/v2/alerts"):
		return "alertmanager"
	case strings.HasSuffix(strings.TrimRight(path, "/"), "/api/v1/alerts"):
		return "prometheus"
	}
	return ""
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const maxAlertBody = 8 << 20

// alertItem is one alert as alerts-firing.json carries it (the /alerts page and the demo generator agree on it).
type alertItem struct {
	Alertname string            `json:"alertname"`
	Severity  string            `json:"severity"`
	State     string            `json:"state"`
	ActiveAt  string            `json:"activeAt"`
	Instance  string            `json:"instance"`
	Value     string            `json:"value"`
	Summary   string            `json:"summary"`
	Labels    map[string]string `json:"labels"`
}

type alertDC struct {
	DC     string      `json:"dc"`
	OK     bool        `json:"ok"`
	Error  string      `json:"error,omitempty"`
	Alerts []alertItem `json:"alerts"`
}

type alertSnapshot struct {
	FetchedAt int64     `json:"fetched_at"`
	DCs       []alertDC `json:"dcs"`
}

// Alerts reads every source and writes one snapshot. A source that fails is recorded as ok=false (the page then
// says "could not read: <name>") and makes the result an error; the snapshot is written in any case.
func Alerts(ctx context.Context, o AlertsOptions) (Result, error) {
	if o.Out == "" || len(o.Sources) == 0 {
		return Result{Status: "ERROR"}, fmt.Errorf("alerts: no sources or no output file configured")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	token := ""
	if o.TokenFile != "" {
		b, err := os.ReadFile(o.TokenFile)
		if err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("alerts: token file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	var roots *x509.CertPool
	if o.CAFile != "" {
		var err error
		if roots, err = rootPool(o.CAFile); err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("alerts: CA file: %w", err)
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsConfig(roots, o.Verify)
	client := &http.Client{Transport: tr, Timeout: o.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	snap := alertSnapshot{DCs: []alertDC{}}
	var failed []string
	firing := 0
	for _, s := range o.Sources {
		start := time.Now()
		items, err := fetchAlerts(ctx, client, s, token)
		d := alertDC{DC: s.Name, OK: err == nil, Alerts: items}
		if d.Alerts == nil {
			d.Alerts = []alertItem{}
		}
		if err != nil {
			d.Error = err.Error()
			failed = append(failed, s.Name)
			Logger.Warn("source failed", "collector", "alerts", "source", s.Name, "err", err, "ms", time.Since(start).Milliseconds())
		} else {
			for _, a := range items {
				if a.State == "firing" {
					firing++
				}
			}
			Logger.Info("source read", "collector", "alerts", "source", s.Name, "alerts", len(items), "ms", time.Since(start).Milliseconds())
		}
		snap.DCs = append(snap.DCs, d)
	}
	snap.FetchedAt = o.Now().Unix()
	data, err := json.MarshalIndent(snap, "", " ")
	if err != nil {
		return Result{Status: "ERROR"}, err
	}
	if err := writeFileAtomic(o.Out, append(data, '\n')); err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("alerts: write %s: %w", o.Out, err)
	}
	r := Result{Status: "OK", Items: len(o.Sources), Summary: fmt.Sprintf("%d firing from %d sources", firing, len(o.Sources))}
	if len(failed) > 0 {
		r.Status = "ERROR"
		r.Summary += "; could not read: " + strings.Join(failed, ", ")
		return r, fmt.Errorf("alerts: could not read %s", strings.Join(failed, ", "))
	}
	return r, nil
}

func fetchAlerts(ctx context.Context, client *http.Client, s AlertSource, token string) ([]alertItem, error) {
	u, err := url.Parse(s.URL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "argus-collector")
	if token != "" {
		if u.Scheme != "https" && !isLoopback(u.Hostname()) {
			return nil, fmt.Errorf("refusing to send the bearer token over plain http to %s", u.Host)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAlertBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxAlertBody {
		return nil, fmt.Errorf("answer larger than %d bytes", maxAlertBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if apiOf(u.Path) == "alertmanager" {
		return parseAlertmanager(body)
	}
	return parsePrometheus(body)
}

// isoTime normalises an RFC 3339 time to 2006-01-02T15:04:05Z (UTC, seconds), as the snapshot always carried it.
func isoTime(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func newItem(labels, annotations map[string]string, state, activeAt, value string) alertItem {
	summary := annotations["summary"]
	if summary == "" {
		summary = annotations["description"]
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return alertItem{Alertname: labels["alertname"], Severity: labels["severity"], State: state, ActiveAt: isoTime(activeAt),
		Instance: labels["instance"], Value: value, Summary: summary, Labels: labels}
}

// parseAlertmanager reads GET /api/v2/alerts: active → firing, unprocessed → pending, suppressed → suppressed.
func parseAlertmanager(body []byte) ([]alertItem, error) {
	var in []struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    string            `json:"startsAt"`
		Status      struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("not an Alertmanager v2 alert list: %w", err)
	}
	out := []alertItem{}
	for _, a := range in {
		state := a.Status.State
		switch state {
		case "active", "":
			state = "firing"
		case "unprocessed":
			state = "pending"
		}
		out = append(out, newItem(a.Labels, a.Annotations, state, a.StartsAt, ""))
	}
	sortAlerts(out)
	return out, nil
}

// parsePrometheus reads GET /api/v1/alerts: {"status":"success","data":{"alerts":[...]}}.
func parsePrometheus(body []byte) ([]alertItem, error) {
	var in struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Alerts []struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
				State       string            `json:"state"`
				ActiveAt    string            `json:"activeAt"`
				Value       string            `json:"value"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("not a Prometheus alert list: %w", err)
	}
	if in.Status != "success" {
		return nil, fmt.Errorf("prometheus: status %q %s", in.Status, in.Error)
	}
	out := []alertItem{}
	for _, a := range in.Data.Alerts {
		out = append(out, newItem(a.Labels, a.Annotations, a.State, a.ActiveAt, a.Value))
	}
	sortAlerts(out)
	return out, nil
}

// sortAlerts gives a stable file: by name, then instance, then start.
func sortAlerts(a []alertItem) {
	sort.SliceStable(a, func(i, j int) bool {
		if a[i].Alertname != a[j].Alertname {
			return a[i].Alertname < a[j].Alertname
		}
		if a[i].Instance != a[j].Instance {
			return a[i].Instance < a[j].Instance
		}
		return a[i].ActiveAt < a[j].ActiveAt
	})
}
