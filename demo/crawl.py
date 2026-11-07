#!/usr/bin/env python3
"""Crawl a running demo: sign in as the demo viewer and the demo admin, follow every same-site link from the start
pages (GET only), and return {url: (status, html)} per role. Used by demo/leakcheck.py and the demo tests.

  python3 -I demo/crawl.py http://127.0.0.1:8080 [--max 1500] [--out DIR]   # prints a status summary
"""
import argparse
import html.parser
import http.cookiejar
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

START = ["/today", "/review", "/review?kind=proposals", "/review?kind=approvals", "/review?kind=lessons", "/review?kind=ideas",
         "/review?kind=replies", "/review?kind=links", "/operations", "/certs", "/backups", "/backups/new", "/capacity",
         "/hygiene", "/incidents", "/problems", "/alerts", "/rca", "/board", "/board/new", "/board/ask", "/qa",
         "/qa?view=messages", "/cicd", "/cicd/failures", "/cicd/stuck", "/cicd/audit", "/cicd/repo", "/inventory", "/vulns",
         "/runs", "/knowledge", "/admin/fix", "/help", "/account", "/admin", "/admin/users", "/admin/board",
         "/admin/outline", "/admin/backup-targets", "/admin/credentials", "/incidents/problems", "/inventory.csv",
         "/cicd/summary.json"]
# old URLs (IA v2): each must answer 301 to a page that answers 200; removed pages (ADR-0016 OD-8) answer 404
MOVED = ["/", "/proposals", "/ideas", "/admin/lessons", "/problems?view=suggestions"]
REMOVED = ["/seo", "/admin/design", "/admin/design/view"]
SKIP = re.compile(r"^/(logout|auth/|static/|favicon|apple-touch)")


class Links(html.parser.HTMLParser):
    def __init__(self):
        super().__init__()
        self.links = []

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        for k in ("href", "src", "action"):
            if tag == "form" and k == "action":
                continue  # forms are POST targets; the crawl only GETs
            if a.get(k):
                self.links.append(a[k])


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


def opener():
    jar = http.cookiejar.CookieJar()
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NoRedirect()), jar


def get(op, url):
    try:
        with op.open(url, timeout=20) as r:
            return r.status, r.read().decode("utf-8", "replace"), dict(r.headers)
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace"), dict(e.headers)


def login(base, user, password):
    op, jar = opener()
    _s, page, _h = get(op, base + "/login")
    m = re.search(r'name="csrf" value="([^"]+)"', page)
    data = urllib.parse.urlencode({"csrf": m.group(1) if m else "", "next": "/", "username": user, "password": password}).encode()
    req = urllib.request.Request(base + "/login", data=data, headers={"Origin": base})
    try:
        op.open(req, timeout=20)
    except urllib.error.HTTPError as e:
        if e.code != 303:
            raise
    if not any(c.name == "opsr_session" or c.name.startswith("opsr") for c in jar):
        raise SystemExit(f"login as {user} failed")
    # the session cookie is Secure: keep it on plain http for the local crawl
    for c in jar:
        c.secure = False
    return op


def crawl(base, user, password, limit=1500):
    op = login(base, user, password)
    seen, queue, out = set(), list(START), {}
    host = urllib.parse.urlsplit(base).netloc
    while queue and len(out) < limit:
        path = queue.pop(0)
        if path in seen:
            continue
        seen.add(path)
        status, body, hdr = get(op, base + path)
        out[path] = (status, body)
        if "html" not in hdr.get("Content-Type", ""):
            continue
        p = Links()
        p.feed(body)
        for link in p.links:
            u = urllib.parse.urlsplit(urllib.parse.urljoin(base + path, html.unescape(link)))
            if u.netloc != host:
                continue
            nxt = u.path + (("?" + u.query) if u.query else "")
            if SKIP.match(u.path) or nxt in seen:
                continue
            queue.append(nxt)
    for path in MOVED:
        status, _b, hdr = get(op, base + path)
        loc = hdr.get("Location", "")
        st2 = get(op, base + loc)[0] if loc.startswith("/") else 0
        out["moved " + path] = (200 if status in (301, 302, 303) and st2 == 200 else status, "")
    for path in REMOVED:
        status, _b, _h = get(op, base + path)
        out["removed " + path] = (200 if status == 404 else status, "")
    return out


def admin_only(path):
    """The pages the app reserves for admins (server.go gate, and help.go for the Settings help page)."""
    return path.startswith("/admin") or path.startswith("/help/settings")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base")
    ap.add_argument("--max", type=int, default=1500)
    ap.add_argument("--user", default=os.environ.get("DEMO_VIEWER", "demo.viewer"))
    ap.add_argument("--admin", default=os.environ.get("DEMO_ADMIN", "demo.admin"))
    ap.add_argument("--password", default=os.environ.get("DEMO_PASSWORD", "northwind-demo-2026"))
    a = ap.parse_args()
    bad = 0
    for u in (a.user, a.admin):
        res = crawl(a.base.rstrip("/"), u, a.password, a.max)
        codes = {}
        for path, (st, _b) in res.items():
            codes.setdefault(st, []).append(path)
        print(u, {k: len(v) for k, v in sorted(codes.items())})
        for st, paths in sorted(codes.items()):
            if u == a.user and st == 403:
                paths = [x for x in paths if not admin_only(x)]  # admin pages answer 403 to a viewer, by design
            if st != 200 and paths:
                bad += len(paths)
                print("  ", st, " ".join(paths[:20]))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
