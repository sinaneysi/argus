// Package cdn is the CDN log job of the marketing module (ADR-0021 D6): a CDN that ships its access logs to an
// Elasticsearch / OpenSearch index usually keeps them only for a short time, so every 5 minutes the job saves the
// counts of the finished 5-minute buckets of the last 55 minutes into ARGUS_DB: requests with a Googlebot user agent
// or that the CDN marks as Google, by who (real = an address in Google's crawler ranges, known = the CDN says Google
// and the ranges do not, fake = the rest) x host x status x firewall rule, action and block x upstream status x cache
// status (cdn_gbot), and per bucket the totals (cdn_buckets). Rows are replaced per bucket, so a rerun never counts
// twice; a bucket the index could not serve, or one without a single document, is a gap (ok=0), never zeros.
package cdn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/marketing/googlebot"
)

const (
	Bucket   = 300
	lookback = 55 * 60 // the index keeps about an hour: older buckets are gone, never try them
	keepDays = 400
)

// Dims are the document fields counted, in cdn_gbot column order (host, status, fw_rule, fw_action, fw_blocked,
// up_status, cache); the index must have them as keyword fields (<field>.keyword).
var Dims = []string{"request_host", "status", "fw_rule_name", "fw_rule_action", "fw_blocked", "upstream_status", "cache_status"}

// Search is one search request on the index: (status, body).
type Search func(ctx context.Context, path string, body []byte) (int, []byte, error)

// Options configure the job.
type Options struct {
	Index  string // the index pattern of the CDN logs
	Search Search
	Ranges func(ctx context.Context) (googlebot.Nets, string)
	Now    func() time.Time
	Log    io.Writer
}

// Query is the aggregation of one bucket.
func Query(t0 int64) map[string]any {
	rng := map[string]any{"range": map[string]any{"@timestamp": map[string]any{"gte": t0 * 1000, "lt": (t0 + Bucket) * 1000, "format": "epoch_millis"}}}
	ua := map[string]any{"wildcard": map[string]any{"http_user_agent.keyword": map[string]any{"value": "*Googlebot*"}}}
	known := map[string]any{"term": map[string]any{"is_known_bot.keyword": "Google"}}
	var terms []map[string]any
	for _, d := range Dims {
		terms = append(terms, map[string]any{"field": d + ".keyword", "missing": "-"})
	}
	return map[string]any{"size": 0, "track_total_hits": true, "query": rng, "aggs": map[string]any{
		"known": map[string]any{"filter": known},
		"g": map[string]any{"filter": map[string]any{"bool": map[string]any{"should": []any{ua, known}, "minimum_should_match": 1}},
			"aggs": map[string]any{
				"ua": map[string]any{"filter": ua},
				"ip": map[string]any{"terms": map[string]any{"field": "remote_addr.keyword", "size": 5000}, "aggs": map[string]any{
					"kb": map[string]any{"terms": map[string]any{"field": "is_known_bot.keyword", "size": 3, "missing": "-"}},
					"k":  map[string]any{"multi_terms": map[string]any{"size": 200, "terms": terms}}}}}}}}
}

// Result is the part of a search answer that is read.
type Result struct {
	Hits struct {
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
	} `json:"hits"`
	Aggs struct {
		Known struct {
			Count int64 `json:"doc_count"`
		} `json:"known"`
		G struct {
			UA struct {
				Count int64 `json:"doc_count"`
			} `json:"ua"`
			IP struct {
				Buckets []struct {
					Key string `json:"key"`
					KB  struct {
						Buckets []struct {
							Key any `json:"key"`
						} `json:"buckets"`
					} `json:"kb"`
					K struct {
						Buckets []struct {
							Key   []any `json:"key"`
							Count int64 `json:"doc_count"`
						} `json:"buckets"`
					} `json:"k"`
				} `json:"buckets"`
			} `json:"ip"`
		} `json:"g"`
	} `json:"aggregations"`
}

// Key is one cdn_gbot row's dimensions: who and Dims.
type Key [8]string

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
	case bool:
		if x {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	}
	return fmt.Sprint(v)
}

// Rows are the counts per key of one answer.
func Rows(r *Result, ns googlebot.Nets) map[Key]int64 {
	out := map[Key]int64{}
	for _, b := range r.Aggs.G.IP.Buckets {
		who := "fake"
		if ns.Contains(b.Key) {
			who = "real"
		} else {
			for _, k := range b.KB.Buckets {
				if text(k.Key) == "Google" {
					who = "known" // the CDN says Google, the range lists do not: worth a look
				}
			}
		}
		for _, k := range b.K.Buckets {
			key := Key{who}
			for i := 0; i < 7 && i < len(k.Key); i++ {
				key[i+1] = cut(text(k.Key[i]), 80)
			}
			out[key] += k.Count
		}
	}
	return out
}

// Run is one run's result.
type Run struct{ Read, Failed int }

// Collect reads every finished bucket not stored yet. All reads failing is an error.
func (o *Options) Collect(ctx context.Context, db *sql.DB) (Run, error) {
	now := time.Now().Unix()
	if o.Now != nil {
		now = o.Now().Unix()
	}
	ns, ver := o.Ranges(ctx)
	last := now - now%Bucket - Bucket // the newest finished bucket, one bucket of slack for late documents
	first := last - lookback + Bucket
	done := map[int64]bool{}
	rows, err := db.Query("SELECT ts FROM cdn_buckets WHERE ok IN (1, 2) AND ts>=?", first)
	if err != nil {
		return Run{}, err
	}
	for rows.Next() {
		var t int64
		rows.Scan(&t)
		done[t] = true
	}
	rows.Close()
	var run Run
	for t := first; t <= last; t += Bucket {
		if done[t] {
			continue
		}
		if ctx.Err() != nil {
			return run, ctx.Err()
		}
		run.Read++
		start := time.Now()
		var res Result
		var data map[Key]int64
		errText := ""
		body, _ := json.Marshal(Query(t))
		st, b, err := o.Search(ctx, "/"+o.Index+"/_search", body)
		switch {
		case err != nil:
			errText = err.Error()
		case st != 200:
			errText = fmt.Sprintf("HTTP %d: %s", st, cut(strings.TrimSpace(string(b)), 160))
		case json.Unmarshal(b, &res) != nil:
			errText = "the answer is not a search result"
		case res.Hits.Total.Value == 0:
			// no document at all in 5 minutes: the forwarding stopped or the index already dropped it
			errText = "no documents in the index for this bucket"
		default:
			data = Rows(&res, ns)
		}
		ms := time.Since(start).Milliseconds()
		tx, err := db.Begin()
		if err != nil {
			return run, err
		}
		tx.Exec("DELETE FROM cdn_gbot WHERE ts=?", t)
		if data == nil {
			run.Failed++
			_, err = tx.Exec("INSERT OR REPLACE INTO cdn_buckets VALUES (?,NULL,NULL,NULL,0,?,?,?)", t, cut(errText, 200), ms, now)
		} else {
			for k, n := range data {
				if _, err = tx.Exec("INSERT INTO cdn_gbot VALUES (?,?,?,?,?,?,?,?,?,?)", t, k[0], k[1], k[2], k[3], k[4], k[5], k[6], k[7], n); err != nil {
					break
				}
			}
			if err == nil {
				_, err = tx.Exec("INSERT OR REPLACE INTO cdn_buckets VALUES (?,?,?,?,1,NULL,?,?)", t, res.Hits.Total.Value,
					res.Aggs.Known.Count, res.Aggs.G.UA.Count, ms, now)
			}
		}
		if err != nil {
			tx.Rollback()
			return run, err
		}
		if err := tx.Commit(); err != nil {
			return run, err
		}
	}
	for _, q := range []string{"DELETE FROM cdn_gbot WHERE ts<?", "DELETE FROM cdn_buckets WHERE ts<?"} {
		if _, err := db.Exec(q, now-keepDays*86400); err != nil {
			return run, err
		}
	}
	if o.Log != nil {
		fmt.Fprintf(o.Log, "cdn: %d bucket(s) read, %d failed, ranges %s (%d nets)\n", run.Read, run.Failed, ver, len(ns))
	}
	if run.Read > 0 && run.Failed == run.Read {
		return run, errors.New("cdn: every bucket failed (see cdn_buckets)")
	}
	return run, nil
}
