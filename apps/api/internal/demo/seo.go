package demo

import "math"

// seo writes the Marketing → SEO data: Googlebot on the three production edges every 15 minutes for 7 days (the
// west edge is a cache edge, so it has a cache hit count), the newest hour of Googlebot 5xx, 35 days of Search
// Console figures, the URL and sitemap checks, and one day of suggestions with their summary. No random draws, so
// the rest of the data set stays as it was.
func (g *Gen) seo() {
	o := g.ops
	host := "www." + DOMAIN
	type edge struct {
		name        string
		total, s5xx int64 // per 15 minutes, now
		p95         float64
		cache       bool
	}
	edges := []edge{{"prod-edge-01", 412, 1, 0.42, false}, {"prod-edge-02", 398, 7, 0.95, false}, {"prod-edge-03", 64, 0, 0.12, true}}
	step := int64(900)
	last := g.now - g.now%step
	n := int64(7 * 24 * 4)
	for i := n; i >= 0; i-- {
		ts := last - i*step
		hour := float64((ts/3600)%24) / 24
		wave := 0.75 + 0.25*math.Sin(2*math.Pi*hour)
		for k, e := range edges {
			total := int64(float64(e.total) * wave)
			s5 := int64(0)
			p95 := 0.38 + 0.02*float64(k) + 0.03*math.Sin(float64(i)/7)
			if e.cache {
				p95 = 0.11 + 0.01*math.Sin(float64(i)/5)
			}
			if i == 0 {
				total, s5, p95 = e.total, e.s5xx, e.p95
			} else if !e.cache && i%37 == 3 {
				s5 = 2
			}
			g.seoSnap(ts, e.name, host, 0, total, e.cache, s5, p95)
			if i == 0 {
				g.seoSnap(ts, e.name, host, 1, e.total-11, e.cache, 0, 0.40+0.02*float64(k))
				g.seoSnap(ts, e.name, host, 7, e.total+23, e.cache, 0, ifF(e.cache, 0.12, 0.41))
			}
		}
	}
	g.ins(o, "seo_errors", "ts", last, "status", "503", "upstream", "10.20.30.21:8081", "prefix", "/category", "n", 6, "alias", "prod-shop-01", "location", "/category")
	g.ins(o, "seo_errors", "ts", last, "status", "502", "upstream", "10.20.30.22:8081", "prefix", "/product", "n", 2, "alias", "prod-shop-02", "location", "/product")
	// Search Console: final days only, so the newest day is 3 days old
	for i := 37; i >= 3; i-- {
		day := g.day(float64(i) * D)
		wk := 1.0
		if (i+2)%7 < 2 {
			wk = 0.82 // the weekend
		}
		clicks := roundi((4200 - 9*float64(i)) * wk)
		impr := roundi((96000 + 310*math.Sin(float64(i))) * wk)
		pos := round(6.4+0.02*float64(i)+0.1*math.Sin(float64(i)/3), 2)
		g.ins(o, "gsc_daily", "date", day, "clicks", clicks, "impressions", impr, "ctr", round(float64(clicks)/float64(impr), 4), "position", pos, "fetched", g.t(2*H))
	}
	urls := []struct{ path, verdict, coverage, robots, fetch string }{
		{"/", "PASS", "Submitted and indexed", "ALLOWED", "SUCCESSFUL"},
		{"/category/headphones", "PASS", "Submitted and indexed", "ALLOWED", "SUCCESSFUL"},
		{"/product/nw-200-wireless", "PASS", "Submitted and indexed", "ALLOWED", "SUCCESSFUL"},
		{"/category/phone-cases", "FAIL", "Server error (5xx)", "ALLOWED", "SERVER_ERROR"},
		{"/blog/choosing-a-laptop", "NEUTRAL", "Crawled - currently not indexed", "ALLOWED", "SUCCESSFUL"},
		{"/search?q=sale", "NEUTRAL", "Blocked by robots.txt", "DISALLOWED", "PAGE_FETCH_STATE_UNSPECIFIED"},
	}
	for _, u := range urls {
		crawl, as := "", ""
		if u.fetch == "SUCCESSFUL" || u.fetch == "SERVER_ERROR" {
			crawl, as = g.iso(26*H), "MOBILE"
		}
		g.ins(o, "gsc_urls", "ts", g.t(2*H), "url", "https://"+host+u.path, "verdict", u.verdict, "coverage", u.coverage,
			"last_crawl", crawl, "crawled_as", as, "robots", u.robots, "fetch_state", u.fetch)
	}
	g.ins(o, "gsc_sitemaps", "path", "https://"+host+"/sitemap.xml", "last_downloaded", g.day(D), "pending", 0, "errors", 0, "warnings", 0, "submitted", 1840, "fetched", g.t(2*H))
	g.ins(o, "gsc_sitemaps", "path", "https://"+host+"/sitemap-blog.xml", "last_downloaded", g.day(2*D), "pending", 0, "errors", 0, "warnings", 3, "submitted", 212, "fetched", g.t(2*H))
	// suggestions of today
	day := g.day(0)
	sugg := []struct{ rule, page, query, title, evidence, action string }{
		{"falling", "/category/headphones", "wireless headphones", "Clicks falling: wireless headphones",
			"312 clicks in 7 days, 401 per 7 days before (-22 %); position 4.1 to 5.3", "Check the page still answers the query: price, stock and the main text. Compare with the pages now above it."},
		{"falling", "/product/nw-200-wireless", "nw-200", "Clicks falling: nw-200", "88 clicks in 7 days, 117 before (-25 %)", "The product is out of stock. Add a back-in-stock date or link the newer model."},
		{"ctr", "/category/laptops", "laptop deals", "Low CTR: laptop deals", "18,400 impressions, CTR 0.9 % at position 3.2 (expected about 10 %)", "Rewrite the title and description with the price range and free delivery."},
		{"ctr", "/category/monitors", "4k monitor", "Low CTR: 4k monitor", "6,950 impressions, CTR 1.2 % at position 2.8", "Put the size range and the lowest price in the title."},
		{"striking", "/category/smart-watches", "smart watch", "Close to the top 3: smart watch", "position 4.6, 22,100 impressions", "Add internal links from the home page and the phone pages to this category."},
		{"striking", "/blog/choosing-a-laptop", "how to choose a laptop", "Close to the top 3: how to choose a laptop", "position 5.2, 9,300 impressions", "Add a short comparison table and link the laptop category."},
		{"bounce", "/category/phone-cases", "phone case", "High bounce from Google: phone case", "71 % bounce, 2,140 sessions from Google", "Show the cases for the most common phones first; the page opens on a sold-out item."},
	}
	rank := map[string]int{}
	for _, s := range sugg {
		g.ins(o, "seo_suggestions", "day", day, "rule", s.rule, "rank", rank[s.rule], "page", "https://"+host+s.page,
			"query", s.query, "title", s.title, "evidence", s.evidence, "action", s.action, "score", 1.0/float64(rank[s.rule]+1))
		rank[s.rule]++
	}
	g.ins(o, "seo_summary", "day", day, "text", "1. Fix the 5xx on /category/phone-cases first: Google cannot fetch it.\n"+
		"2. Rewrite the laptop and monitor titles: high impressions, low CTR.\n3. Link the smart watch category from the home page.",
		"cost_usd", 0.04, "model", "sonnet", "error", "", "at", g.t(5*H))
}

func (g *Gen) seoSnap(ts int64, edge, host string, off, total int64, cache bool, s5 int64, p95 float64) {
	var hits any
	if cache {
		hits = total * 3 / 4
	}
	s2 := total - s5 - total/20 - total/25
	g.ins(g.ops, "seo_snapshots", "ts", ts, "edge", edge, "host", host, "offset_d", off, "total", total, "verified", total*96/100,
		"s2xx", s2, "s3xx", total/20, "s4xx", total/25, "s5xx", s5, "p50", round(p95/3, 3), "p95", round(p95, 3), "hits", hits)
}

// ifF is c ? a : b for floats.
func ifF(c bool, a, b float64) float64 {
	if c {
		return a
	}
	return b
}
