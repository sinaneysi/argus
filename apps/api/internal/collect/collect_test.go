package collect

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() { Logger = slog.New(slog.NewTextHandler(io.Discard, nil)) }

const amBody = `[
 {"labels": {"alertname": "DiskFull", "severity": "critical", "instance": "db-01.example:9100"},
  "annotations": {"summary": "/ above 95 %"}, "startsAt": "2026-10-30T10:00:00.123Z", "status": {"state": "active"}},
 {"labels": {"alertname": "Quiet", "severity": "info", "instance": "x.example:1"},
  "annotations": {"description": "silenced"}, "startsAt": "2026-10-30T09:00:00Z", "status": {"state": "suppressed"}}
]`

const promBody = `{"status": "success", "data": {"alerts": [
 {"labels": {"alertname": "HighLoad", "severity": "warning", "instance": "api-01.example:9100"},
  "annotations": {"summary": "load per vCPU 1.4"}, "state": "pending", "activeAt": "2026-10-30T11:58:00.5Z", "value": "1.4e+00"}
]}}`

func TestAlertsSnapshot(t *testing.T) {
	var gotAuth string
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, amBody)
	}))
	defer am.Close()
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, promBody) }))
	defer prom.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 503) }))
	defer down.Close()

	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	os.WriteFile(tok, []byte("tkn-123\n"), 0o600)
	out := filepath.Join(dir, "alerts-firing.json")
	now := time.Unix(1793000000, 0)
	srcs, errs := ParseAlertSources([]string{"east=" + am.URL + "/api/v2/alerts", "west=" + prom.URL + "/api/v1/alerts"})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	res, err := Alerts(context.Background(), AlertsOptions{Sources: srcs, Out: out, TokenFile: tok, Verify: true, Now: func() time.Time { return now }})
	if err != nil || res.Status != "OK" {
		t.Fatalf("%v %+v", err, res)
	}
	if gotAuth != "Bearer tkn-123" {
		t.Fatalf("token over loopback http: %q", gotAuth)
	}
	var snap alertSnapshot
	data, _ := os.ReadFile(out)
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.FetchedAt != now.Unix() || len(snap.DCs) != 2 || !snap.DCs[0].OK || snap.DCs[0].DC != "east" {
		t.Fatalf("snapshot %s", data)
	}
	a := snap.DCs[0].Alerts[0]
	if a.Alertname == nil || *a.Alertname != "DiskFull" || a.State != "firing" || a.ActiveAt[:19] != "2026-10-30T10:00:00" || a.Summary != "/ above 95 %" || a.Labels["instance"] != "db-01.example:9100" {
		t.Fatalf("alertmanager item %+v", a)
	}
	if snap.DCs[0].Alerts[1].State != "suppressed" || snap.DCs[0].Alerts[1].Summary != "silenced" {
		t.Fatalf("suppressed %+v", snap.DCs[0].Alerts[1])
	}
	p := snap.DCs[1].Alerts[0]
	if p.State != "pending" || p.Value == nil || *p.Value != "1.4e+00" || p.ActiveAt[:19] != "2026-10-30T11:58:00" {
		t.Fatalf("prometheus item %+v", p)
	}
	// one failing source: still written, marked, and an error
	srcs = append(srcs, AlertSource{Name: "south", URL: down.URL + "/api/v2/alerts"})
	res, err = Alerts(context.Background(), AlertsOptions{Sources: srcs, Out: out, Verify: true})
	data, _ = os.ReadFile(out)
	if err == nil || res.Status != "ERROR" || !strings.Contains(string(data), `"error": "HTTP 503"`) {
		t.Fatalf("failing source: %v %+v\n%s", err, res, data)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".alerts-firing.json.tmp-*")); len(left) > 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestAlertsSafety(t *testing.T) {
	if _, errs := ParseAlertSources([]string{"a=" + (&url.URL{Scheme: "https", User: url.User("someone"), Host: "am.example", Path: "/api/v2/alerts"}).String(), "b=ftp://x/api/v2/alerts", "c=https://am.example/metrics", "nourl", "a=https://am.example/api/v2/alerts"}); len(errs) != 5 {
		t.Fatalf("want 5 problems, got %v", errs)
	}
	// a bearer token never goes over plain http to a non-loopback host
	if _, err := fetchAlerts(context.Background(), http.DefaultClient, AlertSource{Name: "x", URL: "http://am.example.invalid/api/v2/alerts"}, "t"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("token over http: %v", err)
	}
	// TLS verification is on: a self-signed server fails unless its CA is given
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, amBody) }))
	defer ts.Close()
	out := filepath.Join(t.TempDir(), "a.json")
	src := []AlertSource{{Name: "tls", URL: ts.URL + "/api/v2/alerts"}}
	if _, err := Alerts(context.Background(), AlertsOptions{Sources: src, Out: out, Verify: true}); err == nil {
		t.Fatal("an untrusted certificate must fail")
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o644)
	if _, err := Alerts(context.Background(), AlertsOptions{Sources: src, Out: out, Verify: true, CAFile: ca}); err != nil {
		t.Fatalf("with the CA file: %v", err)
	}
	// redirects are not followed (a token must not travel to another host)
	rd := httptest.NewServer(http.RedirectHandler("https://elsewhere.invalid/api/v2/alerts", http.StatusFound))
	defer rd.Close()
	if _, err := Alerts(context.Background(), AlertsOptions{Sources: []AlertSource{{Name: "rd", URL: rd.URL + "/api/v2/alerts"}}, Out: out, Verify: true}); err == nil || !strings.Contains(err.Error(), "rd") {
		t.Fatalf("redirect: %v", err)
	}
}

// --- certificates -----------------------------------------------------------------------------------------------

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCA(t *testing.T) testCA {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Example Test CA"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(1000 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return testCA{c, key}
}

func (ca testCA) leaf(t *testing.T, cn string, sans []string, days int) tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn}, DNSNames: sans,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-48 * time.Hour),
		NotAfter: time.Now().Add(time.Duration(days)*24*time.Hour + time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key}
}

// tlsServer accepts TLS handshakes on 127.0.0.1 with cert until the test ends; it returns host:port.
func tlsServer(t *testing.T, cert tls.Certificate) string {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func writePEM(t *testing.T, path string, ders ...[]byte) {
	var b []byte
	for _, d := range ders {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// certFixture: two TLS servers (one shared certificate, one expiring soon), a PEM file, an unreachable port.
func certFixture(t *testing.T) (CertsOptions, string) {
	dir := t.TempDir()
	ca := newCA(t)
	caFile := filepath.Join(dir, "ca.pem")
	writePEM(t, caFile, ca.cert.Raw)
	good := ca.leaf(t, "*.shop.example", []string{"*.shop.example", "shop.example"}, 80)
	soon := ca.leaf(t, "pay.example", []string{"pay.example"}, 5)
	a, b := tlsServer(t, good), tlsServer(t, soon)
	file := filepath.Join(dir, "api.pem")
	writePEM(t, file, ca.leaf(t, "api.example", []string{"api.example"}, 20).Certificate...)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	db := filepath.Join(dir, "ops.db")
	_, port, _ := net.SplitHostPort(b)
	return CertsOptions{Targets: []string{a, b, "localhost:" + port, closed}, Files: []string{file}, DB: db, CAFile: caFile, Timeout: 3 * time.Second}, db
}

func TestCerts(t *testing.T) {
	o, dbPath := certFixture(t)
	dir := filepath.Dir(dbPath)
	o.PinsFile = filepath.Join(dir, "pins.tsv")
	os.WriteFile(o.PinsFile, []byte("# host\texpiry\tlabel\nlocalhost\t2099-01-01\tPayment\tby hand\n"), 0o644)
	var noticed []string
	o.Notify = func(level string, n CertNotice, untrusted bool) { noticed = append(noticed, level+" "+n.What) }
	res, err := Certs(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAIL" || res.Items != 5 {
		t.Fatalf("result %+v", res)
	}
	if len(noticed) != 1 || !strings.HasPrefix(noticed[0], "FAIL pay.example expires in 5 days, used by 2 names") {
		t.Fatalf("notice %v", noticed)
	}
	db, _ := sql.Open("sqlite", dbPath)
	defer db.Close()
	var task, status, summary string
	db.QueryRow("SELECT task, status, summary FROM runs").Scan(&task, &status, &summary)
	if task != "cert-check" || status != "FAIL" || !strings.Contains(summary, "FAIL: pay.example expires in 5 days") ||
		!strings.Contains(summary, "**Payment**: localhost: 5 days") || !strings.Contains(summary, "**Unreachable**: 1 name") {
		t.Fatalf("run %s %s %q", task, status, summary)
	}
	rows, _ := db.Query("SELECT kind, label, status, sev, days_left, issuer, n_names, names, san_mismatch, COALESCE(note,'') FROM cert_groups ORDER BY kind, label")
	got := map[string][]any{}
	for rows.Next() {
		var kind, label, st, sev, issuer, names, mism, note string
		var days sql.NullInt64
		var n int
		rows.Scan(&kind, &label, &st, &sev, &days, &issuer, &n, &names, &mism, &note)
		got[kind+" "+label] = []any{st, sev, days.Int64, issuer, n, names, mism, note}
	}
	if g := got["cert *.shop.example wildcard"]; g == nil || g[0] != "OK" || g[2].(int64) != 80 || g[3] != "Example Test CA" || g[7] != "wildcard" {
		t.Fatalf("wildcard group %v (%v)", g, got)
	}
	if g := got["cert pay.example"]; g == nil || g[0] != "FAIL" || g[1] != "page" || g[2].(int64) != 5 || g[4] != 2 || !strings.Contains(g[6].(string), "localhost") {
		t.Fatalf("expiring group %v", g)
	}
	if g := got["cert api.example"]; g == nil || g[0] != "WARN" || g[2].(int64) != 20 || g[6] != "[]" {
		t.Fatalf("file group %v", g)
	}
	if g := got["pin Payment"]; g == nil || g[0] != "open" || g[2].(int64) != 5 {
		t.Fatalf("pin %v", g)
	}
	found := false
	for k, g := range got {
		if strings.HasPrefix(k, "unreachable 127.0.0.1") {
			found = true
			if g[0] != "UNREACH" || g[1] != "ok" || g[7] == "" {
				t.Fatalf("unreachable %s %v", k, g)
			}
		}
	}
	if !found {
		t.Fatalf("no unreachable row: %v", got)
	}
	var hosts int
	db.QueryRow("SELECT count(*) FROM cert_results WHERE run_id=1").Scan(&hosts)
	if hosts != 5 {
		t.Fatalf("%d cert_results rows", hosts)
	}
	// the chain check: without the CA file the test CA is unknown, which the run summary reports
	o.CAFile = ""
	if _, err := Certs(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("SELECT summary FROM runs WHERE id=2").Scan(&summary)
	if !strings.Contains(summary, "chain not trusted") {
		t.Fatalf("chain note %q", summary)
	}
}

// --- backups ----------------------------------------------------------------------------------------------------

func touch(t *testing.T, path string, size int, age time.Duration) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	m := time.Now().Add(-age)
	os.Chtimes(path, m, m)
}

func backupFixture(t *testing.T) BackupsOptions {
	dir := t.TempDir()
	root := filepath.Join(dir, "backups")
	touch(t, filepath.Join(root, "pg/orders/2026-10-29/orders.dump.age"), 5000, 30*time.Hour)
	touch(t, filepath.Join(root, "pg/orders/2026-10-30/orders.dump.age"), 5200, 2*time.Hour)
	touch(t, filepath.Join(root, "pg/orders/2026-10-30/orders.sha256"), 64, 2*time.Hour)
	touch(t, filepath.Join(root, "mongo/reviews/reviews.archive.age"), 3000, 60*time.Hour)
	touch(t, filepath.Join(root, "files/invoices.tar.age"), 10, time.Hour)
	sets := `{"sets": [
	 {"name": "pg-prod_orders", "path": "` + root + `/pg/orders", "glob": "*.dump.age"},
	 {"name": "mongo-prod_reviews", "path": "` + root + `/mongo/reviews", "max_age_hours": 26},
	 {"name": "files-prod_invoices", "path": "` + root + `/files/invoices.tar.age", "min_bytes": 1000},
	 {"name": "os-prod_snapshots", "path": "` + root + `/opensearch"}]}`
	f := filepath.Join(dir, "backup-sets.json")
	os.WriteFile(f, []byte(sets), 0o644)
	return BackupsOptions{SetsFile: f, DB: filepath.Join(dir, "ops.db")}
}

func TestBackups(t *testing.T) {
	o := backupFixture(t)
	res, err := Backups(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "WARN" || !strings.Contains(res.Summary, "3 of 4 sets need a look: mongo-prod_reviews, files-prod_invoices, os-prod_snapshots") {
		t.Fatalf("result %+v", res)
	}
	db, _ := sql.Open("sqlite", o.DB)
	defer db.Close()
	st := map[string]string{}
	rows, _ := db.Query("SELECT name, status, COALESCE(newest,''), COALESCE(age_h,-1), COALESCE(size,-1), kinds FROM backup_results")
	for rows.Next() {
		var name, status, newest string
		var age, size, kinds int64
		rows.Scan(&name, &status, &newest, &age, &size, &kinds)
		st[name] = status
		if name == "pg-prod_orders" && (!strings.HasSuffix(newest, "2026-10-30/orders.dump.age") || age != 2 || size != 5200 || kinds != 1) {
			t.Fatalf("orders row %s %d %d %d", newest, age, size, kinds)
		}
		if name == "os-prod_snapshots" && (newest != "" || age != -1 || size != -1) {
			t.Fatalf("missing row must have NULLs: %q %d %d", newest, age, size)
		}
	}
	want := map[string]string{"pg-prod_orders": "OK", "mongo-prod_reviews": "STALE", "files-prod_invoices": "SMALL", "os-prod_snapshots": "MISSING"}
	for k, v := range want {
		if st[k] != v {
			t.Errorf("%s: %s, want %s", k, st[k], v)
		}
	}
	// the median of earlier checks: a newest dump far below it is SMALL
	if _, err := Backups(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(filepath.Dir(o.SetsFile), "backups/pg/orders/2026-10-31/orders.dump.age"), 900, time.Minute)
	Backups(context.Background(), o)
	var status string
	var med int64
	db.QueryRow("SELECT status, median_prev7 FROM backup_results WHERE name='pg-prod_orders' AND run_id=3").Scan(&status, &med)
	if status != "SMALL" || med != 5200 {
		t.Fatalf("median rule: %s %d", status, med)
	}
}

func TestBackupSetsValidation(t *testing.T) {
	f := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(f, []byte(`{"sets": [{"name": "a b", "path": "rel"}, {"name": "x", "path": "/x", "glob": "["}]}`), 0o644)
	_, err := LoadBackupSets(f)
	if err == nil || !strings.Contains(err.Error(), "without spaces") || !strings.Contains(err.Error(), "bad glob") {
		t.Fatalf("validation: %v", err)
	}
	os.WriteFile(f, []byte(`{"sets": [], "extra": 1}`), 0o644)
	if _, err := LoadBackupSets(f); err == nil {
		t.Fatal("unknown field must fail")
	}
}

func TestAtomicWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	if err := writeFileAtomic(p, []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(filepath.Join(dir, "missing", "x.json"), []byte("1")); err == nil {
		t.Fatal("a missing directory must fail, never be created")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("entries %v", ents)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestExampleBackupSetsFileIsValid(t *testing.T) {
	sets, err := LoadBackupSets("../../config/backup-sets.example.json")
	if err != nil || len(sets) != 3 || sets[1].MaxAgeHours != 26 || sets[1].MinRatio != 0.5 {
		t.Fatalf("%v %+v", err, sets)
	}
}
