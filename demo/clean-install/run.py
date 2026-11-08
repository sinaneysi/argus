#!/usr/bin/env python3
"""Clean-install test (ADR-0021 D8): start deploy/compose.yml with an EMPTY volume and fake data sources
(demo/clean-install/stubs.py), sign in as the first admin, crawl every page of the frontend and every GET endpoint
of the API, submit every safe action, and report per item: works / not-configured / broken.

The committed baseline docs/CLEAN-INSTALL.md lists what is broken today (the backlog). The run fails only on a
regression: an item that is broken now but not in the baseline. --write rewrites the baseline from this run.

  python3 -I demo/clean-install/run.py [--keep] [--no-build] [--write] [--json out.json] [--from out.json]
  python3 -I demo/clean-install/run.py --base-web http://127.0.0.1:3000 --base-api http://127.0.0.1:8080   # running stack

Development tool only (never shipped). Standard library only.
"""
import argparse
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
HERE = os.path.join(ROOT, "demo", "clean-install")
BASELINE = os.path.join(ROOT, "docs", "CLEAN-INSTALL.md")
PROJECT = "argus-clean"
ADMIN, FIRST_PW, NEW_PW = "admin", "clean-install-first-0001", "clean-install-second-0002"
API_PORT, WEB_PORT = "18180", "13100"

# a page whose source is missing renders this marker (ADR-0021 D5); the error boundary renders ERROR_TEXT
NOT_CONFIGURED = re.compile(r'data-state="not-configured"|"notConfigured"')
ERROR_TEXT = "Something went wrong"
# the fake data sources, connected through Settings -> Data sources (POST /api/v1/admin/sources, the page's own
# endpoint), never through the environment; credentials are obviously fake
STUB = "http://stubs:8099"
CONNECT = [
    {"type": "gitlab", "name": "main", "env": "shared", "purpose": "agent", "url": STUB + "/gitlab", "credential": "fake-gitlab-token-0001"},
    {"type": "jira", "name": "main", "env": "shared", "purpose": "tracker", "url": STUB + "/jira", "credential": "fake-jira-token-0001"},
    {"type": "alertmanager", "name": "main", "env": "shared", "purpose": "alerts", "url": STUB + "/alertmanager"},
    {"type": "matrix", "name": "main", "env": "shared", "purpose": "notify", "url": STUB + "/matrix", "credential": "fake-matrix-token-0001"},
]
# pages fed by those sources: each must show this stub value, else it is "broken" (an empty page that should not be)
EXPECT = {
    "/alerts": "DiskAlmostFull",
    "/admin/sources": "argus-reader",
    "/cicd": "shop/checkout",
    "/admin/sources/catalogue": "shop/checkout",
    "/board": "OPS-1",
}
# actions never submitted: they end the test's own session or account
UNSAFE_POSTS = {"/api/v1/auth/logout", "/api/v1/account", "/api/v1/account/sessions", "/api/v1/admin/users/act",
                "/api/v1/auth/login", "/api/v1/auth/mfa", "/api/v1/account/2fa/disable"}

ENV = """COMPANY_NAME=Clean Install Test
ARGUS_PUBLIC_URL=https://argus.example.test
ARGUS_ADMIN_USER={admin}
ARGUS_ADMIN_INITIAL_PASSWORD={pw}
MFA_REQUIRED_FOR_ACTIONS=0
"""


def compose(*args, check=True):
    cmd = ["docker", "compose", "-p", PROJECT, "-f", os.path.join(ROOT, "deploy", "compose.yml"),
           "-f", os.path.join(HERE, "compose.yml")] + list(args)
    env = dict(os.environ, ARGUS_ENV_FILE=os.path.join(HERE, ".argus.env"), ARGUS_API_PORT=API_PORT,
               ARGUS_WEB_PORT=WEB_PORT)
    return subprocess.run(cmd, env=env, check=check, cwd=ROOT)


class Client:
    """Cookies by hand: the session cookie is Secure, and this test talks plain HTTP to 127.0.0.1."""

    def __init__(self, api, web):
        self.api, self.web, self.jar, self.csrf = api, web, {}, ""

    def _store(self, headers):
        for sc in headers.get_all("Set-Cookie") or []:
            name, _, rest = sc.partition("=")
            val = rest.split(";", 1)[0]
            if val == "" or "Max-Age=0" in sc:
                self.jar.pop(name.strip(), None)
            else:
                self.jar[name.strip()] = val

    def req(self, method, url, body=None, accept="application/json"):
        data = json.dumps(body).encode() if body is not None else None
        r = urllib.request.Request(url, data=data, method=method)
        r.add_header("Accept", accept)
        if self.jar:
            r.add_header("Cookie", "; ".join(f"{k}={v}" for k, v in self.jar.items()))
        if data is not None:
            r.add_header("Content-Type", "application/json")
            r.add_header("X-CSRF-Token", self.csrf)
        opener = urllib.request.build_opener(NoRedirect)
        try:
            with opener.open(r, timeout=30) as resp:
                self._store(resp.headers)
                return resp.status, resp.read().decode("utf-8", "replace"), resp.headers
        except urllib.error.HTTPError as e:
            self._store(e.headers)
            return e.code, e.read().decode("utf-8", "replace"), e.headers
        except Exception as e:  # noqa: BLE001 - a dead endpoint is a finding, not a crash
            return 0, str(e), {}

    def json(self, method, path, body=None):
        code, text, _ = self.req(method, self.api + path, body)
        try:
            return code, json.loads(text)
        except ValueError:
            return code, {"_text": text[:200]}

    def login(self):
        code, d = self.json("GET", "/api/v1/csrf")
        self.csrf = d.get("csrf", "") or self.jar.get("argus_csrf", "")
        for pw in (FIRST_PW, NEW_PW):
            code, d = self.json("POST", "/api/v1/auth/login", {"username": ADMIN, "password": pw, "next": "/today"})
            if code == 200 and d.get("state") == "ok":
                break
        else:
            raise SystemExit(f"sign-in failed: {code} {d}")
        code, d = self.json("GET", "/api/v1/session")
        self.csrf = d.get("csrf", self.csrf)
        code, acc = self.json("GET", "/api/v1/account")
        if acc.get("must_change"):
            code, d = self.json("POST", "/api/v1/account", {"current": FIRST_PW, "new": NEW_PW, "new2": NEW_PW})
            if code != 200:
                raise SystemExit(f"password change failed: {code} {d}")
            code, d = self.json("GET", "/api/v1/session")
            self.csrf = d.get("csrf", self.csrf)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


def pages():
    """Every static route of the frontend (app/(app)/**/page.tsx without a dynamic segment)."""
    base = os.path.join(ROOT, "apps", "web", "app", "(app)")
    out = []
    for d, _, files in os.walk(base):
        if "page.tsx" not in files:
            continue
        rel = os.path.relpath(d, base).replace(os.sep, "/")
        if "[" in rel or rel.startswith("@"):
            continue
        out.append("/" + ("" if rel == "." else rel))
    return sorted(set(out))


def api_routes():
    src = open(os.path.join(ROOT, "apps", "api", "internal", "app", "api.go"), encoding="utf-8").read()
    gets = re.search(r"var apiGETs = map\[string\]apiGetFn\{(.*?)\n\}", src, re.S).group(1)
    posts = re.search(r"func apiIsPost\(path string\) bool \{(.*?)return true", src, re.S).group(1)
    return sorted(set(re.findall(r'"(/api/v1/[^"]+)"', gets))), sorted(set(re.findall(r'"(/api/v1/[^"]+)"', posts)))


def classify_page(code, text, want=None):
    if code >= 500 or code == 0 or ERROR_TEXT in text:
        return "broken", f"HTTP {code}" + (" error page" if ERROR_TEXT in text else "")
    if want and code == 200 and not NOT_CONFIGURED.search(text) and want not in text:
        return "broken", f"no stub data ({want} missing)"
    if code == 404:  # a detail page without its id answers the app's not-found page: a clean answer
        return "works", "HTTP 404 (needs a parameter)"
    if code in (301, 302, 303, 307, 308):
        return "works", f"HTTP {code} redirect"
    if NOT_CONFIGURED.search(text):
        return "not-configured", ""
    return "works", f"HTTP {code}"


def classify_api(code, d):
    if code >= 500 or code == 0:
        return "broken", f"HTTP {code} {str(d.get('error', d.get('_text', '')))[:80]}"
    if isinstance(d, dict) and d.get("notConfigured"):
        return "not-configured", ""
    return "works", f"HTTP {code}"


def run(c):
    rows = []
    for p in pages():
        code, text, _ = c.req("GET", c.web + p, accept="text/html")
        st, why = classify_page(code, text, EXPECT.get(p))
        rows.append({"kind": "page", "item": p, "state": st, "detail": why})
    gets, posts = api_routes()
    for p in gets:
        code, d = c.json("GET", p)
        st, why = classify_api(code, d)
        if code in (400, 404) and st == "works":  # needs an id or a query: answered cleanly
            why += " (needs a parameter)"
        rows.append({"kind": "api", "item": "GET " + p, "state": st, "detail": why})
    for p in posts:
        if p in UNSAFE_POSTS:
            continue
        code, d = c.json("POST", p, {"csrf": c.csrf})
        st, why = classify_api(code, d)
        rows.append({"kind": "action", "item": "POST " + p, "state": st, "detail": why})
    return rows


def connect(c, seconds=150):
    """Connect the stubs through Settings -> Data sources, wait for the worker to apply them and for the first alerts
    snapshot (the pages read what the worker's jobs wrote)."""
    for src in CONNECT:
        code, d = c.json("POST", "/api/v1/admin/sources", dict(src, action="save", mode="add", csrf=c.csrf))
        if code != 200:
            raise SystemExit(f"connect {src['type']}: HTTP {code} {d}")
    end = time.time() + seconds
    while time.time() < end:
        code, d = c.json("GET", "/api/v1/admin/sources")
        kinds = {k["key"]: k["state"] for k in d.get("kinds", [])} if code == 200 else {}
        code, a = c.json("GET", "/api/v1/alerts")
        if all(kinds.get(k) == "ok" for k in ("gitlab", "jira", "alertmanager", "matrix")) and a.get("loaded"):
            return
        time.sleep(3)
    print("warning: sources or the first alerts snapshot not ready:", kinds)


def read_baseline():
    if not os.path.exists(BASELINE):
        return set()
    out = set()
    for line in open(BASELINE, encoding="utf-8"):
        m = re.match(r"\|\s*(page|api|action)\s*\|\s*`([^`]+)`\s*\|\s*broken\s*\|", line)
        if m:
            out.add((m.group(1), m.group(2)))
    return out


def write_baseline(rows):
    counts = {}
    for r in rows:
        counts.setdefault(r["kind"], {}).setdefault(r["state"], 0)
        counts[r["kind"]][r["state"]] += 1
    lines = ["# Clean-install backlog", "",
             "Generated by `python3 -I demo/clean-install/run.py --write` (ADR-0021 D8): `deploy/compose.yml` with an",
             "EMPTY volume and fake GitLab, Jira, Alertmanager and Matrix (`demo/clean-install/stubs.py`), connected",
             "through Settings -> Data sources (the page's own API), not the environment. Every page, API GET endpoint",
             "and safe action is classified `works`, `not-configured` (the \"Connect X to enable\" state of a source",
             "that is not connected, here Claude) or `broken`. A page fed by a connected stub must show a value of the",
             "stub's data (`EXPECT` in `run.py`); an empty one is `broken`. The gate fails only on a regression: an item",
             "that is broken but not listed broken here. Fixing an item and re-running with `--write` shrinks the list.",
             "", "An action counts as `works` when it answers below 500 (an empty form is usually refused with 400).", "",
             "| kind | works | not-configured | broken |", "|---|---|---|---|"]
    for k in ("page", "api", "action"):
        c = counts.get(k, {})
        lines.append(f"| {k} | {c.get('works', 0)} | {c.get('not-configured', 0)} | {c.get('broken', 0)} |")
    lines += ["", "## Broken (the backlog)", "", "| kind | item | state | detail |", "|---|---|---|---|"]
    for r in rows:
        if r["state"] == "broken":
            lines.append(f"| {r['kind']} | `{r['item']}` | broken | {r['detail'].replace('|', '/')} |")
    lines += ["", "## Not configured", "", "| kind | item |", "|---|---|"]
    for r in rows:
        if r["state"] == "not-configured":
            lines.append(f"| {r['kind']} | `{r['item']}` |")
    open(BASELINE, "w", encoding="utf-8").write("\n".join(lines) + "\n")


def wait_up(c, seconds=180):
    end = time.time() + seconds
    while time.time() < end:
        a, _, _ = c.req("GET", c.api + "/healthz")
        w, _, _ = c.req("GET", c.web + "/healthz")
        if a == 200 and w == 200:
            return
        time.sleep(2)
    raise SystemExit("the stack did not come up")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-api")
    ap.add_argument("--base-web")
    ap.add_argument("--keep", action="store_true", help="leave the stack running")
    ap.add_argument("--no-build", action="store_true")
    ap.add_argument("--write", action="store_true", help="rewrite docs/CLEAN-INSTALL.md from this run")
    ap.add_argument("--json", help="write the rows to this file")
    ap.add_argument("--from", dest="from_json", help="use the rows of an earlier --json run (no stack)")
    a = ap.parse_args()
    if a.from_json:
        rows = json.load(open(a.from_json, encoding="utf-8"))
        return report(rows, a)
    own = not a.base_api
    api = a.base_api or f"http://127.0.0.1:{API_PORT}"
    web = a.base_web or f"http://127.0.0.1:{WEB_PORT}"
    if own:
        with open(os.path.join(HERE, ".argus.env"), "w", encoding="utf-8") as f:
            f.write(ENV.format(admin=ADMIN, pw=FIRST_PW))
        compose("down", "-v", "--remove-orphans", check=False)
        compose("up", "-d", *([] if a.no_build else ["--build"]))
    try:
        c = Client(api, web)
        wait_up(c)
        c.login()
        connect(c)
        rows = run(c)
    finally:
        if own and not a.keep:
            compose("logs", "--no-color", "api", "worker", check=False) if os.environ.get("CLEAN_LOGS") else None
            compose("down", "-v", "--remove-orphans", check=False)
    if a.json:
        json.dump(rows, open(a.json, "w"), indent=1)
    return report(rows, a)


def report(rows, a):
    for r in rows:
        if r["state"] != "works":
            print(f"{r['state']:15} {r['kind']:6} {r['item']}  {r['detail']}")
    tally = {}
    for r in rows:
        tally[r["state"]] = tally.get(r["state"], 0) + 1
    print("summary:", ", ".join(f"{k} {v}" for k, v in sorted(tally.items())))
    if a.write:
        write_baseline(rows)
        print("wrote", os.path.relpath(BASELINE, ROOT))
        return 0
    known = read_baseline()
    new = [r for r in rows if r["state"] == "broken" and (r["kind"], r["item"]) not in known]
    if new:
        print("REGRESSION: broken and not in docs/CLEAN-INSTALL.md:")
        for r in new:
            print(f"  {r['kind']} {r['item']}  {r['detail']}")
        return 1
    print("clean-install gate: PASS (no regression against docs/CLEAN-INSTALL.md)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
