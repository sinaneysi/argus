---
title: Marketing
summary: SEO - whether Google can crawl the shop, what Search Console reports, and the day's suggestions.
order: 5
---

## SEO {#seo}

Search health as an operations team sees it: does Googlebot get fast, correct answers from the edges, does Google
index the key URLs, and what can marketing improve today. Read-only. **Where:** Marketing → [SEO](/seo). The page
refreshes every 60 seconds.

Argus does not collect this data. External SEO jobs write it into the evidence database (`ARGUS_DB`): Googlebot
snapshots every 15 minutes, Search Console every 6 hours, suggestions once a day. A section stays empty until its
job has run.

### What you see {#seo-view}

- **Operations summary:** problems that hurt search and that operations can fix, worst first. Googlebot 5xx per
  backend in the last hour, an edge that got slower, a key URL Google cannot fetch or robots.txt blocks, a sitemap
  with errors, and stale Search Console data. A URL that is not indexed for a content reason is INFO: the content
  team owns it.
- **Suggestions:** the day's rule-based suggestions from Search Console: clicks falling, low CTR, close to the top 3,
  and high bounce. "Top actions today" is a short model summary of those rows; check the numbers before you act.
- **Googlebot per edge:** requests, 5xx share and p95 now, 1 day ago and 7 days ago, per edge and host. FAIL is a
  5xx share of 5 % or a p95 at 3 times the 7-day value; WARN is 1 % or 2 times. Google IP is the share of requests
  from Google's crawler range; the rest only claim the name. Two charts show the 5xx share and the worst p95 over 7
  days.
- **Google Search Console:** clicks, impressions and average position per day, and the index state of the key URLs.
  A day appears 2 to 3 days late.

### Send to chat {#seo-send}

**Send to chat** posts the day's top suggestions to the team's chat room. Argus only drops a request file in
`ARGUS_SETTINGS_IN/seo-send/`; the SEO job sends the message and deletes the file. It needs two-factor
authentication, like the other actions. The demo refuses it.
