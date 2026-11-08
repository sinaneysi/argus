package collect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// CertsOptions configure the certificate check.
type CertsOptions struct {
	Targets    []string // host or host:port (443 when no port); probed with a TLS handshake, nothing else is sent
	Files      []string // PEM files: the first certificate is the leaf, the rest its chain
	DB         string   // ARGUS_DB
	CAFile     string   // optional extra roots for the chain check
	WarnDays   int      // below: WARN (default 30)
	FailDays   int      // below: FAIL (default 14)
	PageDays   int      // below: the notice pings (default 7)
	StaleDays  int      // expired longer ago: a warning, not a page (default 14)
	PinsFile   string   // pinned names: host, pinned expiry, label, note (TSV)
	ExpectFile string   // expected name mismatches: host, san-mismatch, reason (TSV)
	// Notify is called after a run that is not OK, with the grouped notice
	Notify  func(level string, n CertNotice, untrusted bool)
	Timeout time.Duration
	Now     func() time.Time
}

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
	now := o.Now()
	var probes []CertProbe
	untrusted := map[string]string{} // gid -> why the chain is not trusted (Argus's own check, in the run summary)
	check := func(chain []*x509.Certificate) *CertInfo {
		info := certInfo(chain[0])
		inter := x509.NewCertPool()
		for _, c := range chain[1:] {
			inter.AddCert(c)
		}
		if _, err := chain[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: now}); err != nil && !isExpiryErr(err) {
			untrusted[certGID(info)] = shortErr(err)
		}
		return info
	}
	for _, p := range res {
		host, port := hostOf(p.name), 443
		if _, ps, err := net.SplitHostPort(p.name); err == nil {
			fmt.Sscan(ps, &port)
		}
		if p.err != nil {
			probes = append(probes, CertProbe{Host: host, Port: port, Error: probeErr(p.err)})
			Logger.Warn("probe failed", "collector", "certs", "target", p.name, "err", p.err)
			continue
		}
		probes = append(probes, CertProbe{Host: host, Port: port, Cert: check(p.chain)})
		Logger.Info("probed", "collector", "certs", "target", p.name, "not_after", p.chain[0].NotAfter.UTC().Format(time.RFC3339))
	}
	for _, f := range o.Files {
		chain, err := readPEMChain(f)
		if err != nil {
			probes = append(probes, CertProbe{Host: f, Port: 443, Error: certCut(err.Error(), 160), File: true})
			Logger.Warn("file failed", "collector", "certs", "file", f, "err", err)
			continue
		}
		probes = append(probes, CertProbe{Host: f, Port: 443, Cert: check(chain), File: true})
	}
	rep := GroupCerts(probes, now, LoadCertPins(o.PinsFile), LoadCertExpect(o.ExpectFile),
		CertLimits{Warn: o.WarnDays, Fail: o.FailDays, Page: o.PageDays, Stale: o.StaleDays})
	n := rep.Notice()
	worst := n.Level
	var bullets []string
	for _, f := range n.Facts {
		bullets = append(bullets, "- **"+f[0]+"**: "+f[1])
	}
	for _, g := range rep.Groups {
		if why, ok := untrusted[g.gid]; ok {
			bullets = append(bullets, "- **"+g.label+"**: chain not trusted: "+why)
			if worst == "OK" {
				worst = "WARN"
			}
		}
	}
	summary := worst + ": " + n.What + "."
	if len(bullets) > 0 {
		summary += "\n\n" + strings.Join(bullets, "\n")
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
	for _, r := range rep.groupRows() {
		args := append([]any{rid}, r[:]...)
		if _, err := tx.ExecContext(ctx, `INSERT INTO cert_groups (run_id, kind, gid, label, status, sev, days_left, not_after, issuer,
		  n_names, names, sans, san_mismatch, note) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
		}
	}
	for _, h := range rep.hostRows() {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cert_results (run_id, host, status, days_left, not_after, issuer) VALUES (?,?,?,?,?,?)`,
			rid, h[1], h[0], h[2], h[3], h[4]); err != nil {
			return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{Status: "ERROR"}, fmt.Errorf("certs: %w", err)
	}
	if o.Notify != nil && worst != "OK" {
		o.Notify(worst, n, len(untrusted) > 0)
	}
	return Result{Status: worst, Summary: worst + ": " + n.What + ".", Items: len(o.Targets) + len(o.Files)}, nil
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
