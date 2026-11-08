package seo

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/marketing/google"
	"github.com/sinaneysi/argus/internal/migrate"
)

// Parity of the rules with the earlier job on synthetic rows (testdata/suggest-in.json -> suggest-golden.json).
func TestRulesParity(t *testing.T) {
	var in struct {
		QP, Recent, Older []google.Row
		GA                []struct {
			Page     string
			Sessions int64
			Bounce   float64
			Events   float64
		}
		Brand string
	}
	b, _ := os.ReadFile("testdata/suggest-in.json")
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	var ga []google.GAPage
	for _, g := range in.GA {
		ga = append(ga, google.GAPage{Page: g.Page, Sessions: g.Sessions, Bounce: g.Bounce, Events: g.Events})
	}
	var want [][]any
	b, _ = os.ReadFile("testdata/suggest-golden.json")
	json.Unmarshal(b, &want)
	got := Rules(in.QP, in.Recent, in.Older, ga, ParseBrand(in.Brand))
	if len(got) != len(want) {
		t.Fatalf("%d suggestions, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		ws := []string{w[0].(string), w[2].(string), w[3].(string), w[4].(string), w[5].(string), w[6].(string)}
		gs := []string{g.Rule, g.Page, g.Query, g.Title, g.Evidence, g.Action}
		for k := range ws {
			if ws[k] != gs[k] {
				t.Errorf("#%d field %d: %q, want %q", i, k, gs[k], ws[k])
			}
		}
		if int(w[1].(float64)) != g.Rank || math.Abs(w[7].(float64)-g.Score) > 1e-6 {
			t.Errorf("#%d rank/score %d %v, want %v %v", i, g.Rank, g.Score, w[1], w[7])
		}
	}
}

func testKey(t *testing.T, tokenURI string) string {
	pk, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(pk)
	pemS := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	b, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "reader@example-project.iam.gserviceaccount.com",
		"private_key": pemS, "token_uri": tokenURI})
	return string(b)
}

// fakeGoogle answers the token, Search Console and GA4 calls with synthetic data.
func fakeGoogle(t *testing.T, gaFails bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		auth := r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/token":
			if !strings.Contains(string(body), "assertion=") {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"access_token": "fake-access-0001"}`)
		case auth != "Bearer fake-access-0001":
			w.WriteHeader(401)
		case strings.HasSuffix(r.URL.Path, "/searchAnalytics/query"):
			var q struct{ Dimensions []string }
			json.Unmarshal(body, &q)
			switch strings.Join(q.Dimensions, ",") {
			case "date":
				io.WriteString(w, `{"rows": [{"keys": ["2026-11-01"], "clicks": 120, "impressions": 4000, "ctr": 0.03, "position": 7.123},
					{"keys": ["2026-11-02"], "clicks": 130, "impressions": 4100, "ctr": 0.0317, "position": 6.9}]}`)
			case "query,page":
				io.WriteString(w, `{"rows": [{"keys": ["blue phone", "https://www.example.com/p/1"], "clicks": 50, "impressions": 10000, "ctr": 0.005, "position": 2}]}`)
			default:
				io.WriteString(w, `{"rows": []}`)
			}
		case r.URL.Path == "/v1/urlInspection/index:inspect":
			if strings.Contains(string(body), "/broken") {
				w.WriteHeader(403)
				return
			}
			io.WriteString(w, `{"inspectionResult": {"indexStatusResult": {"verdict": "NEUTRAL", "coverageState": "Excluded by noindex", "robotsTxtState": "ALLOWED", "pageFetchState": "SUCCESSFUL"}}}`)
		case strings.HasSuffix(r.URL.Path, "/sitemaps"):
			io.WriteString(w, `{"sitemap": [{"path": "https://www.example.com/sitemap.xml", "lastDownloaded": "2026-11-01T00:00:00Z", "errors": "2", "warnings": "0", "contents": [{"type": "web", "submitted": "1200"}]}]}`)
		case strings.HasSuffix(r.URL.Path, ":runReport"):
			if gaFails {
				w.WriteHeader(403)
				return
			}
			io.WriteString(w, `{"rows": [{"dimensionValues": [{"value": "/landing"}], "metricValues": [{"value": "1000"}, {"value": "0.8"}, {"value": "3"}]}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
}

func opsDB(t *testing.T) *sql.DB {
	p := filepath.Join(t.TempDir(), "ops.db")
	if _, err := migrate.ApplyFile(context.Background(), p, migrate.Ops); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", "file:"+p)
	return db
}

func client(t *testing.T, srv *httptest.Server) *google.Client {
	k, err := google.ParseKey(testKey(t, srv.URL+"/token"))
	if err != nil {
		t.Fatal(err)
	}
	return &google.Client{Key: k, Base: srv.URL, AllowHTTP: true, Wait: []time.Duration{0}}
}

func TestCollectAndSuggest(t *testing.T) {
	srv := fakeGoogle(t, true)
	defer srv.Close()
	db := opsDB(t)
	defer db.Close()
	cl := client(t, srv)
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	var log strings.Builder
	res, err := Collect(context.Background(), db, cl, "sc-domain:example.com", []string{"https://www.example.com/", "https://www.example.com/broken"}, now, &log)
	if err != nil || res.Days != 2 || res.Inspected != 1 || len(res.NotPass) != 1 || res.Sitemaps != 1 || !strings.Contains(log.String(), "/broken") {
		t.Fatalf("%+v %v %q", res, err, log.String())
	}
	var pos float64
	var errs, sub int64
	db.QueryRow("SELECT position FROM gsc_daily WHERE date='2026-11-01'").Scan(&pos)
	db.QueryRow("SELECT errors, submitted FROM gsc_sitemaps").Scan(&errs, &sub)
	if pos != 7.12 || errs != 2 || sub != 1200 {
		t.Fatalf("position %v errors %d submitted %d", pos, errs, sub)
	}
	o := SuggestOptions{Property: "sc-domain:example.com", GAProperty: "123", Today: now}
	sug, note, err := Suggest(context.Background(), db, cl, o)
	if err != nil || len(sug) != 1 || sug[0].Rule != "ctr" || !strings.HasPrefix(note, "GA4 skipped") {
		t.Fatalf("%v %q %v", sug, note, err)
	}
	Suggest(context.Background(), db, cl, o) // the same day again: replaced, not doubled
	var n int
	db.QueryRow("SELECT count(*) FROM seo_suggestions").Scan(&n)
	if n != 1 || !HasDay(db, "2026-11-05") {
		t.Fatalf("%d rows", n)
	}
	if m := Message(db); !strings.Contains(m, "Low CTR:\n- Low CTR for «blue phone»") {
		t.Fatalf("message %q", m)
	}
	srv2 := fakeGoogle(t, false)
	defer srv2.Close()
	sug, note, _ = Suggest(context.Background(), db, client(t, srv2), o)
	if note != "" || len(sug) != 2 || sug[1].Rule != "bounce" {
		t.Fatalf("with GA4: %v %q", sug, note)
	}
}

func TestKeyRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer srv.Close()
	cl := client(t, srv)
	if _, err := cl.Sites(context.Background()); err == nil || !strings.Contains(err.Error(), "refused the key") {
		t.Fatalf("%v", err)
	}
	if _, err := google.ParseKey(`{"client_email": "x"}`); err == nil {
		t.Fatal("a key without a private key")
	}
	plain := &google.Client{Key: cl.Key}
	if _, err := plain.Token(context.Background(), google.ScopeSearchConsole); err == nil || !strings.Contains(err.Error(), "https only") {
		t.Fatalf("http token URI without AllowHTTP: %v", err)
	}
}
