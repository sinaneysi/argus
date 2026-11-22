package cdn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/marketing/googlebot"
	"github.com/sinaneysi/argus/internal/migrate"
)

func nets() googlebot.Nets {
	var ns googlebot.Nets
	for _, c := range []string{"66.249.64.0/19", "2001:4860:4801::/48"} {
		_, n, _ := net.ParseCIDR(c)
		ns = append(ns, n)
	}
	return ns
}

// Parity with the earlier job: the same search body, the same rows from the same answer (synthetic fixtures).
func TestParity(t *testing.T) {
	var want, got any
	b, _ := os.ReadFile("testdata/query.json")
	json.Unmarshal(b, &want)
	q, _ := json.Marshal(Query(1761730000))
	json.Unmarshal(q, &got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("query\n got %s\nwant %s", q, b)
	}
	var res Result
	b, _ = os.ReadFile("testdata/search.json")
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	var rows [][]any
	for k, n := range Rows(&res, nets()) {
		r := []any{}
		for _, x := range k {
			r = append(r, x)
		}
		rows = append(rows, append(r, float64(n)))
	}
	sort.Slice(rows, func(i, j int) bool {
		a, _ := json.Marshal(rows[i])
		c, _ := json.Marshal(rows[j])
		return string(a) < string(c)
	})
	var gold [][]any
	b, _ = os.ReadFile("testdata/golden.json")
	json.Unmarshal(b, &gold)
	sort.Slice(gold, func(i, j int) bool {
		a, _ := json.Marshal(gold[i])
		c, _ := json.Marshal(gold[j])
		return string(a) < string(c)
	})
	if !reflect.DeepEqual(rows, gold) {
		t.Errorf("rows\n got %v\nwant %v", rows, gold)
	}
}

func opsDB(t *testing.T) *sql.DB {
	p := filepath.Join(t.TempDir(), "ops.db")
	if _, err := migrate.ApplyFile(context.Background(), p, migrate.Ops); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", "file:"+p)
	return db
}

// Every bucket is read once; an empty answer and a failed search are gaps, never zeros.
func TestCollect(t *testing.T) {
	db := opsDB(t)
	defer db.Close()
	answer, _ := os.ReadFile("testdata/search.json")
	calls := 0
	mode := "ok"
	o := &Options{Index: "cdn-logs-*", Now: func() time.Time { return time.Unix(1761730000, 0) },
		Ranges: func(context.Context) (googlebot.Nets, string) { return nets(), "test" },
		Search: func(_ context.Context, path string, body []byte) (int, []byte, error) {
			calls++
			if path != "/cdn-logs-*/_search" || !strings.Contains(string(body), "multi_terms") {
				t.Fatalf("%s %s", path, body)
			}
			switch mode {
			case "empty":
				return 200, []byte(`{"hits": {"total": {"value": 0}}}`), nil
			case "down":
				return 0, nil, errors.New("connection refused")
			}
			return 200, answer, nil
		}}
	r, err := o.Collect(context.Background(), db)
	if err != nil || r.Read != 11 || calls != 11 {
		t.Fatalf("%+v %v calls %d", r, err, calls)
	}
	if r, _ := o.Collect(context.Background(), db); r.Read != 0 {
		t.Fatal("read again")
	}
	var n, sum int64
	db.QueryRow("SELECT count(DISTINCT ts), sum(n) FROM cdn_gbot").Scan(&n, &sum)
	if n != 11 || sum == 0 {
		t.Fatalf("%d buckets, %d", n, sum)
	}
	for _, m := range []string{"empty", "down"} {
		mode = m
		db.Exec("DELETE FROM cdn_buckets")
		db.Exec("DELETE FROM cdn_gbot")
		r, err := o.Collect(context.Background(), db)
		var gaps, rows int64
		db.QueryRow("SELECT count(*) FROM cdn_buckets WHERE ok=0 AND total IS NULL").Scan(&gaps)
		db.QueryRow("SELECT count(*) FROM cdn_gbot").Scan(&rows)
		if err == nil || r.Failed != 11 || gaps != 11 || rows != 0 {
			t.Fatalf("%s: %+v %v gaps %d rows %d", m, r, err, gaps, rows)
		}
	}
}
