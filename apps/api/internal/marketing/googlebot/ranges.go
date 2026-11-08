package googlebot

// ranges.go: Google's crawler address ranges (the official JSON lists of common crawlers, special-case crawlers and
// user-triggered fetchers), kept in one file in the state folder and fetched again when it is older than a day. A
// failed fetch keeps the old file; with no file at all the main Googlebot block is the seed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// RangeURLs are Google's lists (the default of ARGUS_GOOGLE_RANGES_URLS).
var RangeURLs = []string{
	"https://developers.google.com/static/crawling/ipranges/common-crawlers.json",
	"https://developers.google.com/static/crawling/ipranges/special-crawlers.json",
	"https://developers.google.com/static/crawling/ipranges/user-triggered-fetchers.json",
}

var seed = []string{"66.249.64.0/19"}

type rangeFile struct {
	Prefixes []struct {
		IPv4 string `json:"ipv4Prefix,omitempty"`
		IPv6 string `json:"ipv6Prefix,omitempty"`
	} `json:"prefixes"`
	CreationTime any `json:"creationTime"`
}

// Ranges loads (and refreshes) the range file.
type Ranges struct {
	Path  string
	URLs  []string
	Fetch func(ctx context.Context, url string) (io.ReadCloser, error)
	Now   func() time.Time
	Log   func(string, ...any)
}

func (r *Ranges) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func parseNets(f rangeFile) Nets {
	var ns Nets
	for _, p := range f.Prefixes {
		c := p.IPv4
		if c == "" {
			c = p.IPv6
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			ns = append(ns, n)
		}
	}
	return ns
}

func (r *Ranges) refresh(ctx context.Context) error {
	var all rangeFile
	oldest := ""
	for _, u := range r.URLs {
		rc, err := r.Fetch(ctx, u)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(rc, 8<<20))
		rc.Close()
		if err != nil {
			return err
		}
		var one rangeFile
		if json.Unmarshal(b, &one) != nil || len(one.Prefixes) == 0 {
			return fmt.Errorf("%s: no prefixes", u)
		}
		all.Prefixes = append(all.Prefixes, one.Prefixes...)
		ct, _ := one.CreationTime.(string)
		if len(ct) > 10 {
			ct = ct[:10]
		}
		if oldest == "" || ct < oldest {
			oldest = ct
		}
	}
	all.CreationTime = oldest // the oldest of the lists: the data-quality age
	b, _ := json.Marshal(all)
	tmp := r.Path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.Path)
}

// Load is the ranges and their date ("seed" when only the built-in block is known).
func (r *Ranges) Load(ctx context.Context) (Nets, string) {
	fi, err := os.Stat(r.Path)
	if (err != nil || r.now().Sub(fi.ModTime()) > 24*time.Hour) && r.Fetch != nil && len(r.URLs) > 0 {
		if err := r.refresh(ctx); err != nil && r.Log != nil {
			r.Log("google ranges refresh failed: %s", cutS(err.Error(), 150))
		}
	}
	if b, err := os.ReadFile(r.Path); err == nil {
		var f rangeFile
		if json.Unmarshal(b, &f) == nil {
			if ns := parseNets(f); len(ns) > 0 {
				ct, _ := f.CreationTime.(string)
				return ns, ct
			}
		}
	}
	var ns Nets
	for _, s := range seed {
		_, n, _ := net.ParseCIDR(s)
		ns = append(ns, n)
	}
	return ns, "seed"
}

// ErrNoRanges is returned by a fetcher that is not configured.
var ErrNoRanges = errors.New("no range fetcher")
