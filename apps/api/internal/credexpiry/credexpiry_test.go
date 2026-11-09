package credexpiry

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/incidents"
	"github.com/sinaneysi/argus/internal/migrate"

	_ "modernc.org/sqlite"
)

// The cases are the earlier job's unit tests (test_cred_expiry.py) on the same synthetic data.

const day = int64(86400)
const now = int64(1_800_000_000) // 2027-01-15 08:00 UTC

var th = Thresholds{WarnDays: 30, CritDays: 7, RotateDays: 180, TodayDays: 14}

func ymd(d int64) string { return time.Unix(now+d*day, 0).UTC().Format("2006-01-02") }
func iso(d int64) string { return time.Unix(now+d*day, 0).UTC().Format("2006-01-02T15:04:05.000Z") }

func TestDaysLeft(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
		ok   bool
	}{{ymd(30), 30, true}, {ymd(0), 0, true}, {ymd(-2), -2, true}, {"2027-01-16T05:00:00.000+03:30", 1, true}, {"nonsense", 0, false}} {
		if got, ok := DaysLeft(c.in, now); got != c.want || ok != c.ok {
			t.Errorf("DaysLeft(%q) = %d %v", c.in, got, ok)
		}
	}
	if !neverExpires("9999-12-31T00:00:00.000+00:00") || neverExpires(ymd(10)) {
		t.Error("never expires")
	}
}

func band(d *int64, f func(*Item)) *Item {
	it := &Item{ExpirySource: "api"}
	if d != nil {
		it.ExpiresAt = sp(ymd(*d))
	}
	if f != nil {
		f(it)
	}
	Classify(it, th, now)
	return it
}

func i64(v int64) *int64 { return &v }

func TestBands(t *testing.T) {
	for d, want := range map[int64][2]string{40: {"ok", "OK"}, 31: {"ok", "OK"}, 30: {"warn", "WARN"}, 8: {"warn", "WARN"},
		7: {"crit", "WARN"}, 1: {"crit", "WARN"}, 0: {"expired", "FAIL"}, -5: {"expired", "FAIL"}} {
		it := band(i64(d), nil)
		if it.Status != want[0] || it.Level != want[1] || *it.DaysLeft != int(d) {
			t.Errorf("%d: %s %s", d, it.Status, it.Level)
		}
	}
	if !band(i64(14), nil).Today || band(i64(15), nil).Today || !band(i64(-1), nil).Today {
		t.Error("today")
	}
	it := band(nil, func(it *Item) { it.Rejected, it.HTTP = true, 401 })
	if it.Status != "rejected" || it.Level != "FAIL" || !strings.Contains(it.Detail, "401") || !it.Today {
		t.Errorf("rejected: %+v", it)
	}
	if band(i64(-3), func(it *Item) { it.Rejected, it.HTTP = true, 401 }).Status != "expired" {
		t.Error("rejected past expiry")
	}
	created := func(ago int64, f func(*Item)) *Item {
		return band(nil, func(it *Item) {
			it.CreatedAt = i64(now - ago*day)
			if f != nil {
				f(it)
			}
		})
	}
	if it := created(200, nil); it.Status != "rotate" || it.Band != "unknown" || !strings.Contains(it.Detail, "overdue") {
		t.Errorf("rotate: %+v", it)
	}
	if it := created(10, nil); it.Status != "ok" || *it.RotateIn != 170 || *it.RotateDue != ymd(170) {
		t.Errorf("rotate in: %+v", it)
	}
	if created(100, func(it *Item) { it.RotateDays = 90 }).Status != "rotate" {
		t.Error("own rotation policy")
	}
	if it := created(10, func(it *Item) { it.ExpiresAt = sp("9999-12-31") }); it.Band != "never" || it.ExpirySource != "never" || it.Status != "ok" {
		t.Errorf("never: %+v", it)
	}
	if it := band(nil, func(it *Item) { it.Absent = true }); it.Status != "absent" || it.Level != "OK" || it.Today {
		t.Errorf("absent: %+v", it)
	}
	if band(i64(100), func(it *Item) { it.Error = "timeout" }).Status != "error" || band(i64(3), func(it *Item) { it.Error = "timeout" }).Status != "crit" {
		t.Error("error")
	}
	if f := (Thresholds{WarnDays: 5, CritDays: 9, RotateDays: 180, TodayDays: 14}).Fix(); f.CritDays >= f.WarnDays {
		t.Errorf("fix %+v", f)
	}
}

var sec = map[string]string{"bot": "fake-gl-BOTsecretVALUE0000000001", "ro": "fake-gl-ROsecretVALUE00000000002", "meta": "fake-gl-METAsecretVALUE000000009",
	"expired": "fake-gl-EXPIREDsecretVALUE00012", "grafA": "fake-gs-AFRsecretVALUE000000003", "grafB": "fake-gs-ASTsecretVALUE000000004",
	"jira": "JIRAsecretVALUE00000000000010", "matrix": "fake-mx-PANsecretVALUE0000000005", "claude": "fake-cl-CLAUDEsecretVALUE6"}

func fake(t *testing.T, calls *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("PRIVATE-TOKEN")
		if tok == "" {
			tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		send := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
		switch p := r.URL.Path; {
		case p == "/api/v4/personal_access_tokens/self":
			tab := map[string][2]any{sec["bot"]: {"ops-bot", int64(25)}, sec["ro"]: {"ro", int64(300)}, sec["meta"]: {"meta-reader", int64(5)}}
			x, ok := tab[tok]
			if !ok {
				send(401, map[string]any{"message": "401 Unauthorized"})
				return
			}
			name := x[0]
			if tok == sec["ro"] {
				name = tok // a service that echoes the token: the scrub must catch it
			}
			send(200, map[string]any{"id": 380, "name": name, "scopes": []string{"read_api"}, "active": true, "revoked": false,
				"expires_at": ymd(x[1].(int64)), "created_at": iso(-10), "last_used_at": iso(-1)})
		case p == "/api/user":
			if tok == sec["grafA"] || tok == sec["grafB"] {
				send(200, map[string]any{"id": 0, "login": "sa-1-reader"})
				return
			}
			send(401, map[string]any{})
		case p == "/api/serviceaccounts/search":
			if tok == sec["grafB"] {
				send(200, map[string]any{"serviceAccounts": []any{map[string]any{"id": 7, "login": "sa-1-reader"}}})
				return
			}
			send(403, map[string]any{})
		case p == "/api/serviceaccounts/7/tokens":
			send(200, []any{map[string]any{"id": 3, "name": "t", "created": iso(-40), "expiration": iso(20), "lastUsedAt": iso(0)}})
		case p == "/rest/api/2/myself":
			if tok == sec["jira"] {
				send(200, map[string]any{"name": "ops-bot"})
				return
			}
			send(401, map[string]any{})
		case p == "/rest/pat/latest/tokens":
			send(200, []any{map[string]any{"id": 1, "name": "old", "createdAt": iso(-400), "expiringAt": "2026-01-01T00:00:00.000+0000", "lastAccessedAt": iso(-300)},
				map[string]any{"id": 2, "name": "reader", "createdAt": iso(-200), "expiringAt": "9999-12-31T00:00:00.000+00:00", "lastAccessedAt": iso(0)}})
		case p == "/_matrix/client/v3/account/whoami":
			if tok == sec["matrix"] {
				send(200, map[string]any{"user_id": "@bot:chat.example.test"})
				return
			}
			send(401, map[string]any{})
		default:
			send(404, map[string]any{})
		}
	}))
}

func creds(url string) []Cred {
	return []Cred{
		{ID: "gitlab-bot", Name: "GitLab bot", Kind: "gitlab", Check: "gitlab", URL: url, Value: sec["bot"], Where: "w"},
		{ID: "gitlab-ro", Name: "GitLab read-only", Kind: "gitlab", Check: "gitlab", URL: url, Value: sec["ro"]},
		{ID: "ci-meta", Name: "GitLab CI metadata", Kind: "gitlab", Check: "gitlab", URL: url, Value: sec["meta"]},
		{ID: "backup-plans", Name: "GitLab backup plans", Kind: "gitlab", Check: "gitlab", URL: url, Value: sec["expired"]},
		{ID: "grafana-a", Name: "Grafana A", Kind: "grafana", Check: "grafana", URL: url, Value: sec["grafA"], CreatedAt: now - 200*day, CreatedSource: "file changed"},
		{ID: "grafana-b", Name: "Grafana B", Kind: "grafana", Check: "grafana", URL: url, Value: sec["grafB"]},
		{ID: "jira", Name: "Jira", Kind: "jira", Check: "jira", URL: url, Value: sec["jira"]},
		{ID: "matrix", Name: "Matrix", Kind: "matrix", Check: "matrix", URL: url, Value: sec["matrix"]},
		{ID: "matrix-access", Name: "Matrix access", Kind: "matrix", Check: "matrix", URL: url, Value: "", Optional: true},
		{ID: "claude", Name: "Claude", Kind: "claude", Check: "none", Value: sec["claude"], CreatedAt: now - 10*day, ValidDays: 365},
		{ID: "nvd", Name: "NVD", Kind: "api-key", Check: "none", Value: "NVDsecretVALUE-0000-0000-000011", CreatedAt: now - 400*day},
	}
}

func check(t *testing.T, o *Options, url string, at int64, cs []Cred) map[string]*Item {
	o.Now = func() time.Time { return time.Unix(at, 0) }
	d, err := o.Check(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*Item{}
	for _, it := range d.Items {
		out[it.ID] = it
	}
	return out
}

func noSecret(t *testing.T, text, where string) {
	t.Helper()
	for _, s := range sec {
		if strings.Contains(text, s) || strings.Contains(text, s[:16]) {
			t.Errorf("a secret leaked into %s", where)
		}
	}
}

func TestCheckAndRecord(t *testing.T) {
	var calls []string
	srv := fake(t, &calls)
	defer srv.Close()
	dir := t.TempDir()
	o := &Options{Out: filepath.Join(dir, "credentials.json"), Thresholds: th, Prober: &Prober{AllowHTTP: true}}
	it := check(t, o, srv.URL, now, creds(srv.URL))
	want := map[string]string{"gitlab-bot": "warn", "ci-meta": "crit", "gitlab-ro": "ok", "backup-plans": "rejected", "grafana-a": "rotate",
		"grafana-b": "warn", "jira": "rotate", "matrix": "ok", "matrix-access": "absent", "claude": "ok", "nvd": "rotate"}
	for id, st := range want {
		if it[id].Status != st {
			t.Errorf("%s: %s, want %s (%s)", id, it[id].Status, st, it[id].Detail)
		}
	}
	if *it["gitlab-bot"].DaysLeft != 25 || it["gitlab-bot"].Identity != "ops-bot" || *it["gitlab-bot"].TokenID != 380 ||
		*it["gitlab-bot"].CreatedSource != "api" || *it["gitlab-bot"].LastUsed != now-day {
		t.Errorf("gitlab-bot: %+v", it["gitlab-bot"])
	}
	if it["gitlab-ro"].Identity != "[redacted]" || it["grafana-a"].ExpirySource != "none" || *it["grafana-a"].CreatedSource != "file changed" ||
		*it["grafana-b"].DaysLeft != 20 || it["jira"].Band != "never" || it["jira"].Identity != "ops-bot · reader" ||
		it["matrix"].Identity != "@bot:chat.example.test" || it["claude"].ExpirySource != "estimate" || !it["ci-meta"].Today {
		t.Errorf("details: ro %q grafana %+v jira %+v", it["gitlab-ro"].Identity, it["grafana-b"], it["jira"])
	}
	raw, _ := os.ReadFile(o.Out)
	noSecret(t, string(raw), "credentials.json")
	var doc Doc
	json.Unmarshal(raw, &doc)
	if doc.Items[0].Status != "rejected" || doc.Worst != "FAIL" || doc.Thresholds != th {
		t.Errorf("doc: worst %s first %s", doc.Worst, doc.Items[0].Status)
	}
	if st, _ := os.Stat(o.Out); st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", st.Mode())
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "GET ") {
			t.Errorf("not read-only: %s", c)
		}
	}
	// the bot token is refused later: the last known date stays, status rejected; expired once that date passes
	cs := creds(srv.URL)
	cs[0].Value = "fake-gl-REVOKEDsecretVALUE0014"
	it = check(t, o, srv.URL, now+day, cs)
	if b := it["gitlab-bot"]; b.Status != "rejected" || b.ExpirySource != "last-known" || *b.ExpiresAt != ymd(25) {
		t.Errorf("last known: %+v", b)
	}
	if it = check(t, o, srv.URL, now+30*day, cs); it["gitlab-bot"].Status != "expired" {
		t.Errorf("expired: %s", it["gitlab-bot"].Status)
	}

	// record: incidents and notices for crit and rejected, once
	it = check(t, o, srv.URL, now, creds(srv.URL))
	db := opsDB(t, dir)
	d, _ := Load(o.Out)
	io := &incidents.Options{StateDir: dir}
	r, err := Record(db, d, now, io)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range r.Notices {
		ids = append(ids, n.Item.ID)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "backup-plans,ci-meta" || len(r.Incidents) != 2 {
		t.Errorf("notices %v incidents %v", ids, r.Incidents)
	}
	sev := map[string]string{}
	rows, _ := db.Query(`SELECT subject, severity, dc FROM incidents`)
	for rows.Next() {
		var s, v, dc string
		rows.Scan(&s, &v, &dc)
		sev[s] = v + "/" + dc
	}
	rows.Close()
	if sev["ci-meta"] != "high/CRED" || sev["backup-plans"] != "critical/CRED" {
		t.Errorf("incidents %v", sev)
	}
	if r, _ = Record(db, d, now+3600, io); len(r.Notices) != 0 || len(r.Incidents) != 0 {
		t.Errorf("second run: %+v", r)
	}
	for _, x := range d.Items {
		if x.ID == "ci-meta" {
			x.ExpiresAt = sp(ymd(365))
			Classify(x, th, now+day)
		}
	}
	if r, _ = Record(db, d, now+day, io); len(r.Resolved) != 1 {
		t.Errorf("resolved: %+v", r)
	}
	var st, note string
	db.QueryRow(`SELECT status, resolve_note FROM incidents WHERE subject='ci-meta'`).Scan(&st, &note)
	if st != "resolved" || !strings.Contains(note, "OK again") {
		t.Errorf("ci-meta incident %s %q", st, note)
	}
	var dump strings.Builder
	rows, _ = db.Query(`SELECT COALESCE(title,'') || COALESCE(subject,'') FROM incidents UNION ALL SELECT COALESCE(text,'') FROM incident_events`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		dump.WriteString(s)
	}
	rows.Close()
	noSecret(t, dump.String(), "ops.db")
}

func opsDB(t *testing.T, dir string) *sql.DB {
	p := filepath.Join(dir, "ops.db")
	db, err := sql.Open("sqlite", "file:"+p+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := migrate.Apply(context.Background(), db, "ops", func() int64 { return now }); err != nil {
		t.Fatal(err)
	}
	return db
}
