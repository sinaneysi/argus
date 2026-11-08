package googlebot

// sitemaps.go: the daily read of the site's own sitemaps (GET, one request a second, image and video sitemaps
// skipped): every URL with its lastmod in gw_sitemap, for crawl waste and coverage. The files are read as a stream
// (one <url> block at a time, gzip unpacked on the fly) and parsed without an XML parser, so an entity cannot expand.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	sitemapMax  = 200 << 20 // bytes read of one sitemap file
	sitemapUnz  = 1 << 30   // bytes unpacked of one gzip sitemap
	sitemapKeep = 3 * 86400 // a URL no sitemap lists any more is dropped after this
)

var (
	locRe     = regexp.MustCompile(`<loc>\s*([^<\s]+)\s*</loc>`)
	lastmodRe = regexp.MustCompile(`<lastmod>\s*([^<\s]+)\s*</lastmod>`)
	mediaRe   = regexp.MustCompile(`sitemap-(images|videos)-`)
	closeTags = [][]byte{[]byte("</url>"), []byte("</sitemap>")}
	openTags  = [][]byte{[]byte("<url>"), []byte("<sitemap>")}
)

// splitBlocks splits a sitemap at every </url> or </sitemap>.
func splitBlocks(data []byte, atEOF bool) (int, []byte, error) {
	best, n := -1, 0
	for _, t := range closeTags {
		if i := bytes.Index(data, t); i >= 0 && (best < 0 || i < best) {
			best, n = i, len(t)
		}
	}
	if best >= 0 {
		return best + n, data[:best], nil
	}
	if atEOF {
		return len(data), nil, nil
	}
	return 0, nil, nil
}

// Locs calls fn with every (loc, lastmod) of a sitemap or a sitemap index.
func Locs(r io.Reader, fn func(loc, lastmod string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	sc.Split(splitBlocks)
	for sc.Scan() {
		blk := sc.Bytes()
		start := -1
		for _, t := range openTags {
			if i := bytes.Index(blk, t); i >= 0 && (start < 0 || i < start) {
				start = i + len(t)
			}
		}
		if start < 0 {
			continue
		}
		blk = blk[start:]
		m := locRe.FindSubmatch(blk)
		if m == nil {
			continue
		}
		lm := ""
		if x := lastmodRe.FindSubmatch(blk); x != nil {
			lm = string(x[1])
		}
		fn(strings.ReplaceAll(strings.ToValidUTF8(string(m[1]), "�"), "&amp;", "&"), lm)
	}
	return sc.Err()
}

// openSitemap is the sitemap's body, unpacked when it is gzip.
func openSitemap(rc io.ReadCloser) (io.Reader, error) {
	br := bufio.NewReader(io.LimitReader(rc, sitemapMax))
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		z, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		return io.LimitReader(z, sitemapUnz), nil
	}
	return br, nil
}

var isoLayouts = []string{"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999", "2006-01-02T15:04Z07:00",
	"2006-01-02T15:04", "2006-01-02"}

// ISO is a W3C datetime as unix seconds (UTC when it has no zone); ok false when it does not parse.
func ISO(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	for _, l := range isoLayouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// Sitemaps reads the sitemap index and every sitemap it lists, and stores their URLs.
func (o *Options) Sitemaps(ctx context.Context) (int, int, error) {
	if o.Sitemap == "" || o.Fetch == nil {
		return 0, 0, fmt.Errorf("no sitemap index configured")
	}
	db, err := Open(ctx, o.DB)
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()
	site := siteOf(o.Sitemap)
	now := o.now()
	var subs []string
	if err := o.read(ctx, o.Sitemap, func(loc, _ string) {
		if !mediaRe.MatchString(loc) {
			subs = append(subs, loc)
		}
	}); err != nil {
		return 0, 0, fmt.Errorf("sitemap index: %w", err)
	}
	n := 0
	for _, sm := range subs {
		if ctx.Err() != nil {
			return len(subs), n, ctx.Err()
		}
		if o.Pause > 0 {
			time.Sleep(o.Pause)
		}
		name := sm[strings.LastIndex(sm, "/")+1:]
		var rows [][5]any
		err := o.read(ctx, sm, func(loc, lm string) {
			if !strings.HasPrefix(loc, site) {
				return
			}
			p := loc[len(site):]
			if p == "" {
				p = "/"
			}
			u, _ := Normalize(p)
			var lmv any
			if lm != "" {
				if t, ok := ISO(lm); ok {
					lmv = t
				}
			}
			rows = append(rows, [5]any{u, PClass(u, o.Classes), name, lmv, now})
		})
		if err != nil { // one broken sitemap must not stop the rest
			o.logf("sitemap %s failed: %s", cutS(sm, 120), cutS(err.Error(), 120))
			continue
		}
		if err := inTx(db, func(tx *sql.Tx) error {
			for _, r := range rows {
				if _, err := tx.Exec("INSERT OR REPLACE INTO gw_sitemap VALUES (?,?,?,?,?)", r[0], r[1], r[2], r[3], r[4]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return len(subs), n, err
		}
		n += len(rows)
	}
	if _, err := db.Exec("DELETE FROM gw_sitemap WHERE seen<?", now-sitemapKeep); err != nil {
		return len(subs), n, err
	}
	setState(db, "gw_sitemaps_at", now)
	o.logf("sitemaps: %d sub-sitemaps, %d URLs", len(subs), n)
	return len(subs), n, nil
}

func (o *Options) read(ctx context.Context, u string, fn func(loc, lastmod string)) error {
	rc, err := o.Fetch(ctx, u)
	if err != nil {
		return err
	}
	defer rc.Close()
	r, err := openSitemap(rc)
	if err != nil {
		return err
	}
	return Locs(r, fn)
}

// siteOf is scheme://host of a URL ("https://www.example.com/sitemap.xml" -> "https://www.example.com").
func siteOf(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return u
	}
	if j := strings.Index(u[i+3:], "/"); j >= 0 {
		return u[:i+3+j]
	}
	return u
}

// SiteHost is the host of the sitemap index URL.
func SiteHost(sitemap string) string {
	s := siteOf(sitemap)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// SitemapsDue says whether the last sitemap read is older than a day (or never happened).
func (o *Options) SitemapsDue(ctx context.Context) bool {
	db, err := Open(ctx, o.DB)
	if err != nil {
		return true
	}
	defer db.Close()
	return o.now()-stateInt(db, "gw_sitemaps_at", 0) >= 86400-3600
}
