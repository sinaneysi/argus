package collect

// certgroup.go: the earlier certificate check's grouping and notice (bin/cert_scan.py group(), bin/cert_report.py
// rows_for() and notify_parts()), so that the certificate pages and the notice read the same rows as before.
//
// Names that serve the same certificate (same expiry, issuer and SAN set) form one group: "*.example.com wildcard -
// 18 days - used by 80 names". Names without TLS are listed apart (not certificate failures). Pinned names
// (host, the expiry the issue was opened with, label) get a line of their own until the served certificate expires
// later. Expected name mismatches (host, reason) are explained, not noise. Countdown: WARN below warn days, FAIL
// below fail days, page below page days or expired; EXPIRED more than stale days ago is a warning, not a page.

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // a group id, the same as the earlier check's (not a security use)
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// CertLimits are the countdown thresholds in days.
type CertLimits struct{ Warn, Fail, Page, Stale int }

func (l CertLimits) orDefault() CertLimits {
	if l.Warn <= 0 {
		l.Warn = 30
	}
	if l.Fail <= 0 {
		l.Fail = 14
	}
	if l.Page <= 0 {
		l.Page = 7
	}
	if l.Stale <= 0 {
		l.Stale = 14
	}
	return l
}

// CertInfo is what the check keeps of one served certificate.
type CertInfo struct {
	NotAfter  string   `json:"not_after"` // 2006-01-02T15:04:05Z
	Issuer    string   `json:"issuer"`    // organisation and common name of the issuer
	SubjectCN string   `json:"subject_cn"`
	SANs      []string `json:"sans"` // DNS names, lower case, sorted, unique
}

// CertProbe is one probed name.
type CertProbe struct {
	Host  string    `json:"host"`
	Port  int       `json:"port"`
	Cert  *CertInfo `json:"cert"`
	Error string    `json:"error"`
	File  bool      `json:"-"` // a PEM file: no name to match
}

// CertPin is one pinned name.
type CertPin struct {
	Host, PinnedExpiry, Label, Note string
}

// CertExpect is one expected name mismatch.
type CertExpect struct{ Reason string }

type certGroup struct {
	gid, notAfter, issuer, subjectCN, label, status, sev string
	sans, names, mismatch                                []string
	days                                                 int64
	wildcard                                             bool
}

type certUnreach struct{ host, err string }

type certExpected struct {
	host, reason, gid, state string
	sans                     []string
}

type certPinOut struct {
	CertPin
	days                 any
	notAfter, gid, state string
}

// CertReport is the grouped result of one check.
type CertReport struct {
	Names       int
	Groups      []*certGroup
	Unreachable []certUnreach
	Pins        []certPinOut
	Expected    []certExpected
	Limits      CertLimits
}

// certInfo is what the check keeps of a leaf certificate (the fields the earlier check read with openssl).
func certInfo(c *x509.Certificate) *CertInfo {
	org := ""
	if len(c.Issuer.Organization) > 0 {
		org = strings.TrimSpace(c.Issuer.Organization[0])
	}
	var parts []string
	for _, x := range []string{org, strings.TrimSpace(c.Issuer.CommonName)} {
		if x != "" {
			parts = append(parts, x)
		}
	}
	issuer := strings.Join(parts, " ")
	if issuer == "" {
		issuer = certCut(c.Issuer.String(), 80)
	}
	seen := map[string]bool{}
	var sans []string
	for _, d := range c.DNSNames {
		if d = strings.ToLower(d); !seen[d] {
			seen[d] = true
			sans = append(sans, d)
		}
	}
	sort.Strings(sans)
	return &CertInfo{NotAfter: c.NotAfter.UTC().Format("2006-01-02T15:04:05Z"), Issuer: issuer,
		SubjectCN: strings.ToLower(c.Subject.CommonName), SANs: sans}
}

// probeErr is the earlier check's short reason for a name without TLS.
func probeErr(err error) string {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		return "no DNS"
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.As(err, &opErr) && opErr.Op == "remote error":
		return "tls: handshake failed"
	}
	if strings.Contains(err.Error(), "tls:") {
		return "tls: handshake failed"
	}
	return certCut(shortErr(err), 40)
}

// pyStrRepr is Python's repr() of a str (ASCII names and issuers).
func pyStrRepr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	var b strings.Builder
	b.WriteString(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case string(r) == q:
			b.WriteString(`\` + q)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 32 || r == 127:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(q)
	return b.String()
}

func certGID(c *CertInfo) string {
	var sans []string
	for _, s := range c.SANs {
		sans = append(sans, pyStrRepr(s))
	}
	t := "(" + strings.Join(sans, ", ")
	if len(sans) == 1 {
		t += ","
	}
	t += ")"
	key := "(" + pyStrRepr(c.NotAfter) + ", " + pyStrRepr(c.Issuer) + ", " + t + ")"
	sum := sha1.Sum([]byte(key)) //nolint:gosec // see the import
	return hex.EncodeToString(sum[:])[:10]
}

func sanMatch(host string, sans []string) bool {
	host = strings.ToLower(host)
	for _, s := range sans {
		if s == host {
			return true
		}
		if strings.HasPrefix(s, "*.") && strings.HasSuffix(host, s[1:]) && !strings.Contains(host[:len(host)-len(s)+1], ".") {
			return true
		}
	}
	return false
}

func certDaysLeft(notAfter string, now time.Time) int64 {
	t, err := time.Parse("2006-01-02T15:04:05Z", notAfter)
	if err != nil {
		return 0
	}
	return int64(math.Floor(float64(t.Unix()-now.Unix()) / 86400))
}

func (l CertLimits) status(days int64) string {
	switch {
	case days < int64(-l.Stale):
		return "EXPIRED"
	case days < int64(l.Fail) || days < 0:
		return "FAIL"
	case days < int64(l.Warn):
		return "WARN"
	}
	return "OK"
}

func (l CertLimits) severity(days int64) string {
	switch {
	case days < int64(-l.Stale):
		return "warn"
	case days < int64(l.Page):
		return "page"
	case days < int64(l.Warn):
		return "warn"
	}
	return "ok"
}

func certGroupLabel(g *certGroup) (string, bool) {
	var wild []string
	for _, s := range g.sans {
		if strings.HasPrefix(s, "*.") {
			wild = append(wild, s)
		}
	}
	if len(wild) > 0 {
		best, bestN := "", -1
		for _, w := range wild {
			n := 0
			for _, name := range g.names {
				if sanMatch(name, []string{w}) {
					n++
				}
			}
			// max by (matches, -len): the first of equal keys wins
			if n > bestN || (n == bestN && len(w) < len(best)) {
				best, bestN = w, n
			}
		}
		return best + " wildcard", true
	}
	main := g.subjectCN
	if main == "" {
		if len(g.sans) > 0 {
			main = g.sans[0]
		} else {
			main = g.names[0]
		}
	}
	if extra := len(g.sans) - 1; extra > 0 {
		main += fmt.Sprintf(" +%d SANs", extra)
	}
	return main, false
}

// GroupCerts groups the probed names (the earlier check's group()).
func GroupCerts(results []CertProbe, now time.Time, pins []CertPin, expect map[string]CertExpect, lim CertLimits) *CertReport {
	lim = lim.orDefault()
	rep := &CertReport{Names: len(results), Limits: lim}
	groups := map[string]*certGroup{}
	var order []string
	for _, r := range results {
		c := r.Cert
		if c == nil || c.NotAfter == "" {
			e := r.Error
			if e == "" {
				e = "no certificate"
			}
			rep.Unreachable = append(rep.Unreachable, certUnreach{r.Host, e})
			continue
		}
		gid := certGID(c)
		g := groups[gid]
		if g == nil {
			g = &certGroup{gid: gid, notAfter: c.NotAfter, issuer: c.Issuer, sans: append([]string{}, c.SANs...), subjectCN: c.SubjectCN}
			groups[gid] = g
			order = append(order, gid)
		}
		name := r.Host
		if r.Port != 443 {
			name += ":" + strconv.Itoa(r.Port)
		}
		g.names = append(g.names, name)
		if ex, ok := expect[r.Host]; ok {
			st := "mismatch"
			if sanMatch(r.Host, c.SANs) {
				st = "matches"
			}
			sans := c.SANs
			if len(sans) > 4 {
				sans = sans[:4]
			}
			rep.Expected = append(rep.Expected, certExpected{r.Host, ex.Reason, gid, st, append([]string{}, sans...)})
		} else if !r.File && !sanMatch(r.Host, c.SANs) {
			g.mismatch = append(g.mismatch, r.Host)
		}
	}
	for _, gid := range order {
		g := groups[gid]
		g.days = certDaysLeft(g.notAfter, now)
		sort.Strings(g.names)
		g.label, g.wildcard = certGroupLabel(g)
		g.status, g.sev = lim.status(g.days), lim.severity(g.days)
		rep.Groups = append(rep.Groups, g)
	}
	sort.SliceStable(rep.Groups, func(i, j int) bool {
		a, b := rep.Groups[i], rep.Groups[j]
		if (a.status == "EXPIRED") != (b.status == "EXPIRED") {
			return b.status == "EXPIRED"
		}
		if a.days != b.days {
			return a.days < b.days
		}
		return len(a.names) > len(b.names)
	})
	sort.SliceStable(rep.Unreachable, func(i, j int) bool { return rep.Unreachable[i].host < rep.Unreachable[j].host })
	byHost := map[string]*certGroup{}
	for _, g := range rep.Groups {
		for _, n := range g.names {
			h, _, _ := strings.Cut(n, ":")
			byHost[h] = g
		}
	}
	unreach := map[string]bool{}
	for _, u := range rep.Unreachable {
		unreach[u.host] = true
	}
	for _, p := range pins {
		item := certPinOut{CertPin: p, state: "not checked"}
		if unreach[p.Host] {
			item.state = "unreachable"
		}
		if g := byHost[p.Host]; g != nil {
			item.days, item.notAfter, item.gid = g.days, g.notAfter, g.gid
			item.state = "open"
			if g.notAfter[:10] > p.PinnedExpiry {
				item.state = "renewed"
			}
		}
		rep.Pins = append(rep.Pins, item)
	}
	return rep
}

// hostRows are the per-name rows (cert_results): status, host, days left, expiry, issuer.
func (rep *CertReport) hostRows() [][5]any {
	var out [][5]any
	for _, g := range rep.Groups {
		for _, n := range g.names {
			h, _, _ := strings.Cut(n, ":")
			st := g.status
			bad := st == "FAIL" || st == "EXPIRED"
			if contains(g.mismatch, h) && !bad {
				st = "WARN(SAN)"
			} else if !bad {
				for _, x := range rep.Expected {
					if x.host == h && x.state == "mismatch" {
						st = "WARN(EXPECTED)"
						break
					}
				}
			}
			out = append(out, [5]any{st, n, g.days, g.notAfter, g.issuer})
		}
	}
	for _, u := range rep.Unreachable {
		out = append(out, [5]any{"UNREACH", u.host, nil, "", ""})
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// pyJSON is json.dumps of a list of strings (", " separators, ASCII escapes).
func pyJSON(l []string) string {
	if len(l) == 0 {
		return "[]"
	}
	var parts []string
	for _, s := range l {
		b, _ := json.Marshal(s)
		parts = append(parts, asciiJSON(string(b)))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func asciiJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r > 126 {
			if r > 0xffff {
				r -= 0x10000
				fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
			continue
		}
		b.WriteRune(r)
	}
	return strings.NewReplacer(`<`, "<", `>`, ">", `&`, "&").Replace(b.String())
}

// groupRows are the cert_groups rows (the earlier rows_for(), without the run id).
func (rep *CertReport) groupRows() [][13]any {
	var out [][13]any
	for _, g := range rep.Groups {
		note := ""
		if g.wildcard {
			note = "wildcard"
		}
		out = append(out, [13]any{"cert", g.gid, g.label, g.status, g.sev, g.days, g.notAfter, g.issuer, int64(len(g.names)),
			pyJSON(g.names), pyJSON(g.sans), pyJSON(g.mismatch), note})
	}
	for _, u := range rep.Unreachable {
		out = append(out, [13]any{"unreachable", "", u.host, "UNREACH", "ok", nil, "", "", int64(1), pyJSON([]string{u.host}), "[]", "[]", u.err})
	}
	for _, p := range rep.Pins {
		out = append(out, [13]any{"pin", p.gid, p.Label, p.state, "", p.days, p.notAfter, p.PinnedExpiry, int64(1), pyJSON([]string{p.Host}), "[]", "[]", p.Note})
	}
	for _, x := range rep.Expected {
		st, sev := "MATCHES", "info"
		if x.state == "mismatch" {
			st, sev = "WARN(EXPECTED)", "warn"
		}
		out = append(out, [13]any{"expect", x.gid, x.host, st, sev, nil, "", "", int64(1), pyJSON([]string{x.host}), pyJSON(x.sans), "[]", x.reason})
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// CertNotice is the severity and the text of a check (the earlier notify_parts()): from the data, never from a
// model's words. page only for a live certificate below the page days; long-expired certificates, SAN mismatches,
// open pins and expected mismatches warn; unreachable names are listed, never a failure.
type CertNotice struct {
	Sev, Level, What string
	Facts            [][2]string
	Names, Certs     int
	Unreachable      int
}

var certRank = map[string]int{"ok": 0, "info": 1, "warn": 2, "page": 3}

// Notice is notify_parts() of the report, in the order the stored rows are read back (expired last, then by days
// left, unknown last, most names first).
func (rep *CertReport) Notice() CertNotice {
	certs := append([]*certGroup(nil), rep.Groups...)
	sort.SliceStable(certs, func(i, j int) bool {
		a, b := certs[i], certs[j]
		if (a.status == "EXPIRED") != (b.status == "EXPIRED") {
			return b.status == "EXPIRED"
		}
		if a.days != b.days {
			return a.days < b.days
		}
		return len(a.names) > len(b.names)
	})
	var live, stale []*certGroup
	for _, c := range certs {
		if c.status == "EXPIRED" {
			stale = append(stale, c)
		} else {
			live = append(live, c)
		}
	}
	sev := "ok"
	for _, c := range certs {
		s := c.sev
		if s == "" {
			s = "ok"
		}
		if certRank[s] > certRank[sev] {
			sev = s
		}
	}
	raise := func() {
		if certRank[sev] < certRank["warn"] {
			sev = "warn"
		}
	}
	for _, c := range certs {
		if len(c.mismatch) > 0 {
			raise()
		}
	}
	var facts [][2]string
	pins := append([]certPinOut(nil), rep.Pins...)
	sort.SliceStable(pins, func(i, j int) bool { return pinLess(pins[i], pins[j]) })
	for _, p := range pins {
		if p.state == "open" {
			raise()
			facts = append(facts, [2]string{p.Label, fmt.Sprintf("%s: %v days (%s)", p.Host, p.days, first10(p.notAfter))})
		}
	}
	for _, p := range pins {
		if p.state == "renewed" {
			facts = append(facts, [2]string{p.Label, fmt.Sprintf("%s renewed, now %s", p.Host, first10(p.notAfter))})
		}
	}
	for _, x := range rep.Expected {
		if x.state == "mismatch" {
			raise()
			r := x.reason
			if r == "" {
				r = "known"
			}
			facts = append(facts, [2]string{"Expected name mismatch", x.host + ": " + r})
		} else {
			facts = append(facts, [2]string{"Expected mismatch gone", x.host + " now matches its certificate (remove it from the expected mismatches file)"})
		}
	}
	for _, c := range live {
		if c.status == "OK" && len(c.mismatch) == 0 {
			continue
		}
		t := fmt.Sprintf("%d days · used by %s", c.days, plural(len(c.names), "name"))
		if len(c.mismatch) > 0 {
			t += fmt.Sprintf(" · SAN mismatch %d", len(c.mismatch))
		}
		facts = append(facts, [2]string{c.label, t})
	}
	if len(stale) > 0 {
		var p []string
		for _, c := range stale[:min(3, len(stale))] {
			p = append(p, fmt.Sprintf("%s (%d d, %d)", c.label, -c.days, len(c.names)))
		}
		t := strings.Join(p, ", ")
		if len(stale) > 3 {
			t += fmt.Sprintf(" +%d", len(stale)-3)
		}
		facts = append(facts, [2]string{"Expired long ago", t})
	}
	if len(rep.Unreachable) > 0 {
		facts = append(facts, [2]string{"Unreachable", plural(len(rep.Unreachable), "name") + " (not certificate failures)"})
	}
	names := len(rep.Unreachable)
	for _, c := range certs {
		names += len(c.names)
	}
	var soon []*certGroup
	for _, c := range live {
		if c.status != "OK" {
			soon = append(soon, c)
		}
	}
	var what string
	if len(soon) > 0 {
		c0 := soon[0]
		if c0.days >= 0 {
			what = fmt.Sprintf("%s expires in %d days, used by %s", c0.label, c0.days, plural(len(c0.names), "name"))
		} else {
			what = fmt.Sprintf("%s EXPIRED %d days ago", c0.label, -c0.days)
		}
		if len(soon) > 1 {
			mx := soon[0].days
			for _, c := range soon {
				mx = max(mx, c.days)
			}
			what += fmt.Sprintf("; %d more within %d days", len(soon)-1, mx+1)
		}
	} else {
		what = plural(len(certs), "certificate") + " on " + plural(names, "name") + " OK"
		if len(live) > 0 {
			nxt := live[0].days
			for _, c := range live {
				nxt = min(nxt, c.days)
			}
			what += fmt.Sprintf(", soonest %d days", nxt)
		}
	}
	level := map[string]string{"page": "FAIL", "warn": "WARN"}[sev]
	if level == "" {
		level = "OK"
	}
	return CertNotice{Sev: sev, Level: level, What: what, Facts: facts, Names: names, Certs: len(certs), Unreachable: len(rep.Unreachable)}
}

// pinLess is the stored order of pin rows: by status 'EXPIRED' (never), days left (unknown last).
func pinLess(a, b certPinOut) bool {
	an, bn := a.days == nil, b.days == nil
	if an != bn {
		return bn
	}
	if !an && a.days.(int64) != b.days.(int64) {
		return a.days.(int64) < b.days.(int64)
	}
	return false
}

func first10(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

// LoadCertPins reads the pins file (host, pinned expiry, label, note; tab-separated, # comments).
func LoadCertPins(path string) []CertPin {
	var out []CertPin
	readTSV(path, func(p []string) {
		if len(p) >= 3 {
			pin := CertPin{Host: strings.ToLower(strings.TrimSpace(p[0])), PinnedExpiry: strings.TrimSpace(p[1]), Label: strings.TrimSpace(p[2])}
			if len(p) > 3 {
				pin.Note = strings.TrimSpace(p[3])
			}
			out = append(out, pin)
		}
	})
	return out
}

// LoadCertExpect reads the expected mismatches file (host, "san-mismatch", reason).
func LoadCertExpect(path string) map[string]CertExpect {
	out := map[string]CertExpect{}
	readTSV(path, func(p []string) {
		if len(p) >= 2 && strings.TrimSpace(p[1]) == "san-mismatch" {
			e := CertExpect{}
			if len(p) > 2 {
				e.Reason = strings.TrimSpace(p[2])
			}
			out[strings.ToLower(strings.TrimSpace(p[0]))] = e
		}
	})
	return out
}

func readTSV(path string, fn func([]string)) {
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		fn(strings.Split(line, "\t"))
	}
}

// ReadCertDomains reads a domains file: one host[:port] per line (port 443 by default), # comments; duplicates once.
func ReadCertDomains(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		host, port, _ := strings.Cut(line, ":")
		host = strings.TrimRight(strings.ToLower(host), ".")
		p := 443
		if n, err := strconv.Atoi(port); err == nil {
			p = n
		}
		t := host + ":" + strconv.Itoa(p)
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, sc.Err()
}

func certCut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var (
	certNameRe   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}$`)
	certListenRe = regexp.MustCompile(`\b443\b|\bssl\b`)
)

// ServiceMapDomains are the public TLS names of a service map (the earlier domain list built from the nginx
// repository): every domain with a server block that listens on 443 or ssl, a real name (no IP, no wildcard, no
// regex), that resolves in DNS when resolve is set. Sorted by registered domain, then name.
func ServiceMapDomains(ctx context.Context, path string, resolve bool) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Domains map[string]struct {
			Blocks []struct {
				Listen []string `json:"listen"`
			} `json:"blocks"`
		} `json:"domains"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var names []string
	for d, v := range doc.Domains {
		n := strings.TrimRight(strings.ToLower(d), ".")
		if len(n) < 4 || len(n) > 253 || !certNameRe.MatchString(n) || net.ParseIP(n) != nil {
			continue
		}
		tls := false
		for _, bl := range v.Blocks {
			for _, l := range bl.Listen {
				if certListenRe.MatchString(l) {
					tls = true
				}
			}
		}
		if tls {
			names = append(names, n)
		}
	}
	reg := func(n string) string {
		p := strings.Split(n, ".")
		return strings.Join(p[len(p)-2:], ".")
	}
	sort.Slice(names, func(i, j int) bool {
		if a, b := reg(names[i]), reg(names[j]); a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	if !resolve {
		return names, nil
	}
	ok := make([]bool, len(names))
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			a, err := net.DefaultResolver.LookupHost(c, n)
			ok[i] = err == nil && len(a) > 0
		}(i, n)
	}
	wg.Wait()
	var out []string
	for i, n := range names {
		if ok[i] {
			out = append(out, n)
		}
	}
	return out, nil
}
