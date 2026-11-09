package collect

// s3.go: a minimal read-only S3 client for the backup check: ListObjectsV2 (path style) signed with AWS Signature
// Version 4. No SDK. Only GET requests, bounded answers, no redirects. Works with AWS S3, MinIO and other
// S3-compatible stores.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// S3 is one S3-compatible endpoint with a read-only key.
type S3 struct {
	Endpoint  string // https://host[:port]
	Region    string // default us-east-1
	AccessKey string
	SecretKey string
	HTTP      *http.Client
	Now       func() time.Time
	PageSize  int // max-keys per request (default 1000)
}

// S3Object is one listed object.
type S3Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

const (
	maxS3Page    = 4 << 20
	maxS3Objects = 200000
	emptySHA256  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func s3Escape(s string, slash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' || (slash && c == '/') {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSHA(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// sign adds the SigV4 headers to a GET request without a body.
func (s *S3) sign(req *http.Request, now time.Time) {
	region := s.Region
	if region == "" {
		region = "us-east-1"
	}
	amz := now.UTC().Format("20060102T150405Z")
	day := amz[:8]
	req.Header.Set("x-amz-date", amz)
	req.Header.Set("x-amz-content-sha256", emptySHA256)
	host := req.URL.Host
	canonical := strings.Join([]string{"GET", s3Escape(req.URL.Path, true), canonicalQuery(req.URL.Query()),
		"host:" + host + "\nx-amz-content-sha256:" + emptySHA256 + "\nx-amz-date:" + amz + "\n",
		"host;x-amz-content-sha256;x-amz-date", emptySHA256}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amz + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA([]byte("AWS4"+s.SecretKey), day)
	k = hmacSHA(k, region)
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.AccessKey+"/"+scope+
		", SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature="+sig)
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var cq []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			cq = append(cq, s3Escape(k, false)+"="+s3Escape(v, false))
		}
	}
	return strings.Join(cq, "&")
}

type listResult struct {
	Contents []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

type s3Error struct {
	Code string `xml:"Code"`
}

// List lists every object under bucket/prefix (recursively), at most 200000.
func (s *S3) List(ctx context.Context, bucket, prefix string) ([]S3Object, error) {
	if s.Now == nil {
		s.Now = time.Now
	}
	cl := s.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	page := s.PageSize
	if page <= 0 || page > 1000 {
		page = 1000
	}
	var out []S3Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {strconv.Itoa(page)}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		u := strings.TrimRight(s.Endpoint, "/") + "/" + bucket
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.URL.RawQuery = canonicalQuery(q) // the same encoding as the signature (%20, never +)
		s.sign(req, s.Now())
		resp, err := cl.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing failed: %s", strings.ReplaceAll(err.Error(), s.Endpoint, "<endpoint>"))
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxS3Page+1))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(body) > maxS3Page {
			return nil, fmt.Errorf("listing page larger than %d bytes", maxS3Page)
		}
		if resp.StatusCode != http.StatusOK {
			var e s3Error
			xml.Unmarshal(body, &e)
			if e.Code != "" {
				return nil, fmt.Errorf("listing refused: HTTP %d %s", resp.StatusCode, cutText(e.Code, 60))
			}
			return nil, fmt.Errorf("listing refused: HTTP %d", resp.StatusCode)
		}
		var r listResult
		if err := xml.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("unreadable listing: %v", err)
		}
		for _, c := range r.Contents {
			t, err := time.Parse(time.RFC3339Nano, c.LastModified)
			if err != nil {
				continue
			}
			out = append(out, S3Object{Key: c.Key, Size: c.Size, LastModified: t})
		}
		if len(out) > maxS3Objects {
			return nil, fmt.Errorf("more than %d objects", maxS3Objects)
		}
		if !r.IsTruncated || r.NextContinuationToken == "" {
			return out, nil
		}
		token = r.NextContinuationToken
	}
}

func cutText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
