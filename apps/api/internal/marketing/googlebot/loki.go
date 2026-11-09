package googlebot

// loki.go: the edge groups (which log streams to read and how to parse them) and the Loki reader. Loki is reached
// through a Grafana data source (its datasource proxy, GET only), the same Grafana the agent reads.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Group is one edge group: a LogQL stream selector and the format of its lines.
//
//	json   nginx escape=json access log with the fields remote_addr, status, request_time, body_bytes_sent,
//	       server_name, upstream, request_uri, http_user_agent (the JSON may mix "k":"v" and "k": "v")
//	cache  a cache edge's short JSON log: ip, s (status), rt (request time), b (bytes), c (cache status), u (URI),
//	       ua (user agent); it has no host field, so the group names the site host it serves
type Group struct {
	Label    string
	Parser   string
	Selector string
	Host     string // cache parser: the site host
}

// ParseGroups reads the edge groups file: label<TAB>parser<TAB>selector[<TAB>host] per line, # comments. The first
// group is the primary one: the detector counts an hour as complete when every bucket of it is stored.
func ParseGroups(r io.Reader) ([]Group, error) {
	var out []Group
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 || len(f) > 4 {
			return nil, fmt.Errorf("line %d: want label<TAB>parser<TAB>selector[<TAB>host]", n)
		}
		g := Group{Label: strings.TrimSpace(f[0]), Parser: strings.TrimSpace(f[1]), Selector: strings.TrimSpace(f[2])}
		if len(f) == 4 {
			g.Host = strings.ToLower(strings.TrimSpace(f[3]))
		}
		switch {
		case !labelRe.MatchString(g.Label) || seen[g.Label]:
			return nil, fmt.Errorf("line %d: the label must be unique, letters, digits and - only", n)
		case g.Parser != "json" && g.Parser != "cache":
			return nil, fmt.Errorf("line %d: parser must be json or cache", n)
		case !strings.HasPrefix(g.Selector, "{") || !strings.HasSuffix(g.Selector, "}") || strings.ContainsAny(g.Selector, "|`\n"):
			return nil, fmt.Errorf("line %d: the selector must be one LogQL stream selector {...}", n)
		case g.Parser == "cache" && !hostRe.MatchString(g.Host):
			return nil, fmt.Errorf("line %d: a cache group names the site host it serves", n)
		}
		seen[g.Label] = true
		out = append(out, g)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no edge group")
	}
	return out, nil
}

// LoadGroupsFile reads the edge groups file.
func LoadGroupsFile(path string) ([]Group, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseGroups(bytes.NewReader(b))
}

// uaFilter is the line filter: every Google crawler user agent.
const uaFilter = "Googlebot|Google-InspectionTool|AdsBot-Google|Storebot-Google|GoogleOther|Mediapartners-Google" +
	"|FeedFetcher-Google|APIs-Google|Google-Read-Aloud|Google-Site-Verification|Google-Extended"

func field(k, capture string) string {
	return "| regexp `\"" + k + "\": ?\"(?P<" + k + ">" + capture + ")\"` "
}

const jsonStr = `(?:[^"\\\\]|\\\\.)*` // a JSON string body, escapes included (RE2: no repeat count > 1000)

// Pipeline is the LogQL after the selector: the fields as nine tab-separated values, and only the edge host label
// kept (hostLabel, the stream label that names the edge host).
func (g Group) Pipeline(hostLabel string) string {
	if g.Parser == "cache" {
		return `| json ra="ip", st="s", rt="rt", bb="b", c="c", u="u", ua="ua" ` +
			`| line_format "{{.ra}}\t{{.st}}\t{{.rt}}\t{{.bb}}\t` + g.Host + `\tcache\t{{.c}}\t{{.u}}\t{{.ua}}" | keep ` + hostLabel
	}
	return field("remote_addr", `[^"]*`) + field("status", "[0-9]*") + field("request_time", `[^"]*`) +
		"| regexp `\"body_bytes_sent\": ?\"?(?P<bb>[0-9]*)` " + field("server_name", `[^"]*`) + field("upstream", `[^"]*`) +
		field("request_uri", jsonStr) + field("http_user_agent", jsonStr) +
		`| line_format "{{.remote_addr}}\t{{.status}}\t{{.request_time}}\t{{.bb}}\t{{.server_name}}\t{{.upstream}}\t-` +
		`\t{{.request_uri}}\t{{.http_user_agent}}" | keep ` + hostLabel
}

// Query is the full LogQL of a group.
func (g Group) Query(hostLabel string) string {
	return g.Selector + ` |~ "` + uaFilter + `" ` + g.Pipeline(hostLabel)
}

var (
	labelRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	hostRe      = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	HostLabelRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// Stats count one run's reads.
type Stats struct {
	Bytes, Queries, Lines, Failed int64
}

// Getter is one GET to Grafana: (status, body).
type Getter func(ctx context.Context, path string) (int, []byte, error)

// Loki reads lines through a Grafana's datasource proxy.
type Loki struct {
	Get       Getter
	UID       string // the Loki data source; "" = the first Loki data source of the Grafana
	HostLabel string // the stream label that names the edge host (default hostname)
	MaxLines  int    // Loki's max_entries_limit_per_query (default 5000): a fuller window is split in half
}

func (l *Loki) uid(ctx context.Context) (string, error) {
	if l.UID != "" {
		return l.UID, nil
	}
	st, b, err := l.Get(ctx, "/api/frontend/settings")
	if err != nil {
		return "", err
	}
	if st != 200 {
		return "", fmt.Errorf("grafana /api/frontend/settings: HTTP %d", st)
	}
	var s struct {
		Datasources map[string]struct {
			Type string `json:"type"`
			UID  string `json:"uid"`
		} `json:"datasources"`
	}
	if json.Unmarshal(b, &s) != nil {
		return "", errors.New("grafana /api/frontend/settings: not JSON")
	}
	best := ""
	for name, d := range s.Datasources {
		if d.Type == "loki" && d.UID != "" && (best == "" || name < best) {
			best, l.UID = name, d.UID
		}
	}
	if l.UID == "" {
		return "", errors.New("the Grafana data source has no Loki data source")
	}
	return l.UID, nil
}

// Lines calls fn for every line of the group in [t0, t1), in chunks. A window over the line cap is split in half
// until it fits; fn sees each chunk once.
func (l *Loki) Lines(ctx context.Context, g Group, t0, t1 int64, st *Stats, fn func([]Line)) error {
	uid, err := l.uid(ctx)
	if err != nil {
		return err
	}
	limit := l.MaxLines
	if limit <= 0 {
		limit = 5000
	}
	hl := l.HostLabel
	if hl == "" {
		hl = "hostname"
	}
	q := url.Values{"query": {g.Query(hl)}, "start": {strconv.FormatInt(t0*1e9, 10)}, "end": {strconv.FormatInt(t1*1e9-1, 10)},
		"limit": {strconv.Itoa(limit)}, "direction": {"forward"}}
	code, b, err := l.Get(ctx, "/api/datasources/proxy/uid/"+url.PathEscape(uid)+"/loki/api/v1/query_range?"+q.Encode())
	if err != nil {
		return err
	}
	st.Bytes += int64(len(b))
	st.Queries++
	if code != 200 {
		return fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(b[:min(len(b), 300)])))
	}
	var d struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return errors.New("loki: the answer is not JSON")
	}
	b = nil
	if d.Status != "success" {
		return fmt.Errorf("loki: %s", cutS(d.Error, 200))
	}
	n := 0
	for _, r := range d.Data.Result {
		n += len(r.Values)
	}
	if n >= limit {
		if t1-t0 <= 1 {
			return fmt.Errorf("more than %d lines in one second", limit)
		}
		m := (t0 + t1) / 2
		if err := l.Lines(ctx, g, t0, m, st, fn); err != nil {
			return err
		}
		return l.Lines(ctx, g, m, t1, st, fn)
	}
	out := make([]Line, 0, n)
	for _, r := range d.Data.Result {
		label := r.Stream[hl]
		if label == "" {
			label = "?"
		}
		for _, v := range r.Values {
			ns, _ := strconv.ParseInt(v[0], 10, 64)
			out = append(out, Line{Label: label, TS: ns / 1e9, Text: v[1]})
		}
	}
	fn(out)
	return nil
}

func cutS(s string, n int) string { return runeCut(s, n) }
