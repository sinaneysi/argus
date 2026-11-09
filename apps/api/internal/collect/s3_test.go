package collect

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The signature of the AWS documentation's ListObjects example (Signature Version 4, header-based).
func TestS3SignatureKnownVector(t *testing.T) {
	s := &S3{AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", Region: "us-east-1"}
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", nil)
	s.sign(req, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("signature\n got %s\nwant %s", got, want)
	}
}

type fakeObj struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	LastModified string `json:"lastModified"`
}

// fakeS3 serves ListObjectsV2 of the listing fixture (bucket "db-backups" etc. under source "store"), two keys per
// page, and refuses requests without a signature.
func fakeS3(t *testing.T, listing map[string][]fakeObj, calls *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Method != http.MethodGet || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=ro-key/") {
			w.WriteHeader(403)
			fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
			return
		}
		bucket := strings.Trim(r.URL.Path, "/")
		prefix := r.URL.Query().Get("prefix")
		var all []fakeObj
		for set, objs := range listing {
			if set != "store/"+bucket+"/"+prefix {
				continue
			}
			for _, o := range objs {
				all = append(all, fakeObj{prefix + o.Key, o.Size, o.LastModified})
			}
		}
		if len(all) == 0 && bucket == "missing" {
			w.WriteHeader(404)
			fmt.Fprint(w, `<Error><Code>NoSuchBucket</Code></Error>`)
			return
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })
		start, _ := strconv.Atoi(r.URL.Query().Get("continuation-token"))
		max, _ := strconv.Atoi(r.URL.Query().Get("max-keys"))
		end := start + max
		if end > len(all) {
			end = len(all)
		}
		type content struct {
			Key          string
			Size         int64
			LastModified string
		}
		res := struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			Contents              []content
			IsTruncated           bool
			NextContinuationToken string `xml:",omitempty"`
		}{IsTruncated: end < len(all)}
		for _, o := range all[start:end] {
			res.Contents = append(res.Contents, content{o.Key, o.Size, o.LastModified})
		}
		if res.IsTruncated {
			res.NextContinuationToken = strconv.Itoa(end)
		}
		xml.NewEncoder(w).Encode(res)
	}))
}

// Parity with the earlier MinIO check (scripts/backup-check.sh with a fake mc on the same listing): the same status,
// newest object, age, size, median and kinds for every set.
func TestBackupsS3Parity(t *testing.T) {
	var listing map[string][]fakeObj
	b, _ := os.ReadFile("testdata/backups-s3/listing.json")
	if err := json.Unmarshal(b, &listing); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := fakeS3(t, listing, &calls)
	defer srv.Close()
	dir := t.TempDir()
	db := filepath.Join(dir, "ops.db")
	now := time.Unix(1767000000, 0)
	s3 := &S3{Endpoint: srv.URL, AccessKey: "ro-key", SecretKey: "fake-secret-0001", HTTP: srv.Client(), PageSize: 2}
	res, err := Backups(context.Background(), BackupsOptions{SetsFile: "testdata/backups-s3/sets.tsv", DB: db,
		S3: map[string]S3Lister{"store": s3}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "WARN" || res.Items != 7 || calls < 10 {
		t.Fatalf("result %+v, %d calls", res, calls)
	}
	d, _ := sql.Open("sqlite", "file:"+db)
	defer d.Close()
	rows, err := d.Query(`SELECT status, name, coalesce(newest, '-'), coalesce(age_h, '-'), coalesce(size, '-'), coalesce(median_prev7, '-'), kinds FROM backup_results ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		v := make([]any, 7)
		p := make([]any, 7)
		for i := range v {
			p[i] = &v[i]
		}
		rows.Scan(p...)
		var s []string
		for _, x := range v {
			s = append(s, fmt.Sprint(x))
		}
		got = append(got, strings.Join(s, "\t"))
	}
	want, _ := os.ReadFile("testdata/backups-s3/expected.tsv")
	wl := strings.Split(strings.TrimSpace(string(want)), "\n")[1:]
	if strings.Join(got, "\n") != strings.Join(wl, "\n") {
		t.Fatalf("rows differ\n got %s\nwant %s", strings.Join(got, "\n"), strings.Join(wl, "\n"))
	}
}

func TestBackupSetsS3Validation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sets.json")
	os.WriteFile(p, []byte(`{"sets":[{"name":"a","s3":"store"},{"name":"b","s3":"store/bk/x","path":"/srv/x"},{"name":"c","s3":"store/bk","min_size":"big"}]}`), 0o644)
	_, err := LoadBackupSets(p)
	if err == nil || !strings.Contains(err.Error(), "set a:") || !strings.Contains(err.Error(), "not both") || !strings.Contains(err.Error(), "min_size") {
		t.Fatalf("got %v", err)
	}
	os.WriteFile(p, []byte(`{"sets":[{"name":"ok","s3":"store/bk/daily/"}]}`), 0o644)
	if s, err := LoadBackupSets(p); err != nil || s[0].MinSize != "auto" {
		t.Fatalf("got %v %v", s, err)
	}
}
