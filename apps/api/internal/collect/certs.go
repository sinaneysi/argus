package collect

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// CertsOptions configure the certificate check.
type CertsOptions struct {
	Targets  []string // host or host:port (443 when no port); probed with a TLS handshake, nothing else is sent
	Files    []string // PEM files: the first certificate is the leaf, the rest its chain
	DB       string   // ARGUS_DB
	CAFile   string   // optional extra roots for the chain check
	WarnDays int      // below: WARN (default 30)
	FailDays int      // below: FAIL (default 14)
	Timeout  time.Duration
	Now      func() time.Time
}

// one probed certificate and the names that served it
type certSeen struct {
	leaf     *x509.Certificate
	chainErr error
	names    []string
	mismatch []string
}

type unreachable struct{ name, err string }

// Certs probes every target and file and records one "cert-check" run, grouped by certificate.
func Certs(ctx context.Context, o CertsOptions) (Result, error) {
	if len(o.Targets) == 0 && len(o.Files) == 0 {
		return Result{Status: "ERROR"}, fmt.Errorf("certs: no targets and no files configured")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.WarnDays <= 0 {
		o.WarnDays = 30
	}
	if o.FailDays <= 0 {
		o.FailDays = 14
	}
	started := o.Now()
	roots, err := rootPool(o.CAFile)
	if err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("certs: CA file: %w", err)
	}
	groups := map[string]*certSeen{}
	var order []string
	var down []unreachable
	add := func(name string, chain []*x509.Certificate, mismatch bool) {
		leaf := chain[0]
		sum := sha256.Sum256(leaf.Raw)
		gid := hex.EncodeToString(sum[:])[:12]
		g := groups[gid]
		if g == nil {
			inter := x509.NewCertPool()
			for _, c := range chain[1:] {
				inter.AddCert(c)
			}
			_, verr := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: o.Now()})
			g = &certSeen{leaf: leaf, chainErr: verr}
			groups[gid] = g
			order = append(order, gid)
		}
		g.names = append(g.names, name)
		if mismatch {
			g.mismatch = append(g.mismatch, name)
		}
	}

	// TLS probes, at most 8 at a time
	type probe struct {
		name  string
		chain []*x509.Certificate
		err   error
	}
	res := make([]probe, len(o.Targets))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, t := range o.Targets {
		wg.Add(1)
		go func(i int, t string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			chain, err := probeTLS(ctx, t, o.Timeout)
			res[i] = probe{t, chain, err}
		}(i, t)
	}
	wg.Wait()
	for _, p := range res {
		host := hostOf(p.name)
		if p.err != nil {
			down = append(down, unreachable{host, shortErr(p.err)})
			Logger.Warn("probe failed", "collector", "certs", "target", p.name, "err", p.err)
			continue
		}
		add(host, p.chain, p.chain[0].VerifyHostname(host) != nil)
		Logger.Info("probed", "collector", "certs", "target", p.name, "not_after", p.chain[0].NotAfter.UTC().Format(time.RFC3339))
	}
	for _, f := range o.Files {
		chain, err := readPEMChain(f)
		if err != nil {
			down = append(down, unreachable{f, err.Error()})
			Logger.Warn("file failed", "collector", "certs", "file", f, "err", err)
			continue
		}
		add(f, chain, false)
	}

	type row struct {
		gid, label, status, sev, notAfter, issuer, note string
		days                                            int64
		names, sans, mism                               []string
	}
	var rows []row
	now := o.Now()
	for _, gid := range order {
		g := groups[gid]
		days := int64(math.Floor(g.leaf.NotAfter.Sub(now).Hours() / 24))
		status := "OK"
		switch {
		case days < -30:
			status = "EXPIRED"
		case days < int64(o.FailDays):
			status = "FAIL"
		case days < int64(o.WarnDays):
			status = "WARN"
		}
		var notes []string
		if g.chainErr != nil && !isExpiryErr(g.chainErr) {
			notes = append(notes, "chain not trusted: "+shortErr(g.chainErr))
			if status == "OK" {
				status = "WARN"
			}
		}
		if len(g.mismatch) > 0 && status == "OK" {
			status = "WARN"
		}
		sev := "warn"
		if status == "OK" {
			sev = "ok"
		}
		sort.Strings(g.names)
		sort.Strings(g.mismatch)
		rows = append(rows, row{gid: gid, label: certLabel(g.leaf), status: status, sev: sev, days: days,
			notAfter: g.leaf.NotAfter.UTC().Format("2006-01-02 15:04:05"), issuer: issuerName(g.leaf),
			names: g.names, sans: sansOf(g.leaf), mism: g.mismatch, note: strings.Join(notes, "; ")})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].days < rows[j].days })

	// run status and the summary shown under "Agent summary"
	worst := "OK"
	var bullets []string
	for _, r := range rows {
		if r.status == "FAIL" {
			worst = "FAIL"
		} else if r.status == "WARN" && worst == "OK" {
			worst = "WARN"
		}
		switch {
		case r.status == "OK":
		case r.days < 0:
			bullets = append(bullets, fmt.Sprintf("- **%s** expired %d days ago.", r.label, -r.days))
		default:
			b := fmt.Sprintf("- **%s** expires in %d days.", r.label, r.days)
			if r.note != "" {
				b += " " + r.note + "."
			}
			if len(r.mism) > 0 {
				b += " Name mismatch: " + strings.Join(r.mism, ", ") + "."
			}
			bullets = append(bullets, b)
		}
	}
	for _, d := range down {
		if worst == "OK" {
			worst = "WARN"
		}
		bullets = append(bullets, fmt.Sprintf("- `%s` unreachable: %s.", d.name, d.err))
	}
	summary := fmt.Sprintf("OK: %d certificates, all valid for at least %d days.", len(rows), o.WarnDays)
	if worst != "OK" {
		need := 0
		for _, r := range rows {
			if r.status != "OK" && r.status != "EXPIRED" {
				need++
			}
		}
		summary = fmt.Sprintf("%s: %d certificates need a look, %d targets unreachable.\n\n%s", worst, need, len(down), strings.Join(bullets, "\n"))
	}

	db, err := openDB(o.DB)
	if err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
	}
	defer tx.Rollback()
	rid, err := insertRun(ctx, tx, "cert-check", started, worst, summary)
	if err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
	}
	js := func(l []string) string {
		if l == nil {
			l = []string{}
		}
		b, _ := json.Marshal(l)
		return string(b)
	}
	for _, r := range rows {
		var note any
		if r.note != "" {
			note = r.note
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cert_groups (run_id, kind, gid, label, status, sev, days_left, not_after, issuer,
		  n_names, names, sans, san_mismatch, note) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, rid, "cert", r.gid, r.label, r.status, r.sev,
			r.days, r.notAfter, r.issuer, len(r.names), js(r.names), js(r.sans), js(r.mism), note); err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
		}
		for _, n := range r.names {
			if _, err := tx.ExecContext(ctx, `INSERT INTO cert_results (run_id, host, status, days_left, not_after, issuer) VALUES (?,?,?,?,?,?)`,
				rid, n, r.status, r.days, r.notAfter, r.issuer); err != nil {
				return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
			}
		}
	}
	for _, d := range down {
		sum := sha256.Sum256([]byte(d.name))
		if _, err := tx.ExecContext(ctx, `INSERT INTO cert_groups (run_id, kind, gid, label, status, sev, days_left, not_after, issuer,
		  n_names, names, sans, san_mismatch, note) VALUES (?,?,?,?,?,?,NULL,NULL,NULL,1,?,'[]','[]',?)`, rid, "unreachable",
			hex.EncodeToString(sum[:])[:12], d.name, "FAIL", "warn", js([]string{d.name}), d.err); err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cert_results (run_id, host, status, days_left, not_after, issuer) VALUES (?,?,?,NULL,NULL,NULL)`,
			rid, d.name, "UNREACHABLE"); err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
	}
	return Result{Status: worst, Summary: strings.SplitN(summary, "\n", 2)[0], Items: len(o.Targets) + len(o.Files)}, nil
}

// hostOf is the name part of host[:port] (IPv6 in brackets allowed).
func hostOf(t string) string {
	if h, _, err := net.SplitHostPort(t); err == nil {
		return h
	}
	return strings.Trim(t, "[]")
}

// probeTLS does one TLS handshake and returns the peer's chain. It does not verify (an expired or untrusted
// certificate must still be reported); Certs checks the chain and the name itself. Nothing is sent after the
// handshake.
func probeTLS(ctx context.Context, target string, timeout time.Duration) ([]*x509.Certificate, error) {
	host, port := hostOf(target), "443"
	if _, p, err := net.SplitHostPort(target); err == nil {
		port = p
	}
	d := &net.Dialer{Timeout: timeout}
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10} //nolint:gosec // a probe: the chain is verified below
	if net.ParseIP(host) == nil {
		cfg.ServerName = host
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(cctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	chain := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return nil, errors.New("no certificate presented")
	}
	return chain, nil
}

func readPEMChain(path string) ([]*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var chain []*x509.Certificate
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue // a key in the same file is skipped, never read further
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, err
		}
		chain = append(chain, c)
	}
	if len(chain) == 0 {
		return nil, errors.New("no PEM certificate in the file")
	}
	return chain, nil
}

func certLabel(c *x509.Certificate) string {
	name := c.Subject.CommonName
	if name == "" && len(c.DNSNames) > 0 {
		name = c.DNSNames[0]
	}
	if name == "" {
		name = c.Subject.String()
	}
	if strings.HasPrefix(name, "*.") {
		name += " (wildcard)"
	}
	return name
}

func issuerName(c *x509.Certificate) string {
	if c.Issuer.CommonName != "" {
		return c.Issuer.CommonName
	}
	return c.Issuer.String()
}

func sansOf(c *x509.Certificate) []string {
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

func isExpiryErr(err error) bool {
	var ce x509.CertificateInvalidError
	return errors.As(err, &ce) && ce.Reason == x509.Expired
}

// shortErr is an error without the "dial tcp ..." prefix noise, at most 160 characters.
func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i > 0 && strings.HasPrefix(s, "dial tcp") {
		s = s[i+2:]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}
