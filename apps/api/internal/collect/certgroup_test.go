package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Parity with the earlier check's group(), rows_for() and notify_parts() on synthetic probe results
// (testdata/certs/golden.json, made by running the earlier modules). Listed difference: the notice names the expected
// mismatches file by its role, not by the earlier installation's path.
func TestCertGroupParity(t *testing.T) {
	b, err := os.ReadFile("testdata/certs/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases map[string]struct {
		Input struct {
			Results []CertProbe `json:"results"`
			Pins    []struct {
				Host, Label, Note string
				Pinned            string `json:"pinned_expiry"`
			} `json:"pins"`
			Expect map[string]struct{ Reason string } `json:"expect"`
		} `json:"input"`
		Rows   [][]any  `json:"rows"`
		TSV    []string `json:"tsv"`
		Notice *struct {
			Sev, Level, What string
			Facts            [][2]string
			Names, Certs     int
			Unreachable      int `json:"unreachable"`
		} `json:"notice"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1767000000, 0)
	for name, c := range cases {
		var pins []CertPin
		for _, p := range c.Input.Pins {
			pins = append(pins, CertPin{Host: p.Host, PinnedExpiry: p.Pinned, Label: p.Label, Note: p.Note})
		}
		ex := map[string]CertExpect{}
		for h, e := range c.Input.Expect {
			ex[h] = CertExpect{Reason: e.Reason}
		}
		rep := GroupCerts(c.Input.Results, now, pins, ex, CertLimits{})
		// cert_groups rows
		var got [][]any
		for _, r := range rep.groupRows() {
			row := []any{float64(1)}
			for _, v := range r {
				switch x := v.(type) {
				case int64:
					row = append(row, float64(x))
				default:
					row = append(row, x)
				}
			}
			got = append(got, row)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.Rows) {
			t.Errorf("%s: cert_groups rows\n got %v\nwant %v", name, got, c.Rows)
		}
		// cert_results rows (the TSV's status, host, days_left, not_after, issuer)
		var tsv []string
		for _, h := range rep.hostRows() {
			d := ""
			if h[2] != nil {
				d = fmt.Sprint(h[2])
			}
			tsv = append(tsv, fmt.Sprintf("%v\t%v\t%s\t%v\t%v", h[0], h[1], d, h[3], h[4]))
		}
		var want []string
		for _, l := range c.TSV[1:] {
			p := strings.Split(l, "\t")
			want = append(want, strings.Join(p[:5], "\t"))
		}
		if strings.Join(tsv, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: per-name rows\n got %s\nwant %s", name, strings.Join(tsv, "\n"), strings.Join(want, "\n"))
		}
		if c.Notice == nil {
			continue
		}
		n := rep.Notice()
		for i := range c.Notice.Facts {
			c.Notice.Facts[i][1] = strings.ReplaceAll(c.Notice.Facts[i][1], "config/cert-expect.tsv", "the expected mismatches file")
		}
		if n.Sev != c.Notice.Sev || n.Level != c.Notice.Level || n.What != c.Notice.What || n.Names != c.Notice.Names ||
			n.Certs != c.Notice.Certs || n.Unreachable != c.Notice.Unreachable || fmt.Sprint(n.Facts) != fmt.Sprint(c.Notice.Facts) {
			t.Errorf("%s: notice\n got %+v\nwant %+v", name, n, *c.Notice)
		}
	}
}

func TestCertDomainSources(t *testing.T) {
	dir := t.TempDir()
	sm := dir + "/service-map.json"
	os.WriteFile(sm, []byte(`{"domains": {
	 "shop.example.com": {"blocks": [{"listen": ["443 ssl"]}]},
	 "www.shop.example.com": {"blocks": [{"listen": ["80"]}, {"listen": ["[::]:443", "ssl http2"]}]},
	 "plain.example.com": {"blocks": [{"listen": ["80"]}]},
	 "10.0.0.1": {"blocks": [{"listen": ["443"]}]},
	 "~^api\\..*$": {"blocks": [{"listen": ["443"]}]},
	 "a.example.net": {"blocks": [{"listen": ["8443 ssl"]}]}}}`), 0o644)
	got, err := ServiceMapDomains(context.Background(), sm, false)
	if err != nil || strings.Join(got, ",") != "shop.example.com,www.shop.example.com,a.example.net" {
		t.Fatalf("service map names %v %v", got, err)
	}
	df := dir + "/domains.txt"
	os.WriteFile(df, []byte("# names\nShop.Example.com.\nshop.example.com:443\npay.example.net:8443 # payments\n\n"), 0o644)
	ds, err := ReadCertDomains(df)
	if err != nil || strings.Join(ds, ",") != "shop.example.com:443,pay.example.net:8443" {
		t.Fatalf("domains %v %v", ds, err)
	}
}
