#!/usr/bin/env python3
"""Fake data sources for the clean-install test: GitLab, Jira, Alertmanager and Matrix behind one HTTP port, each
under its own path prefix, and for the marketing module: Grafana with Loki (edge access lines of Google crawlers),
Google's token and search APIs, an OpenSearch with a CDN log index, the site's sitemaps and Google's crawler ranges.
Synthetic data only; every write is answered and forgotten, except the GitLab project CI/CD variables (scope develop),
which the gitlab.ci-variable capabilities of actd read and change: they are kept in memory and every call is
recorded (GET /gitlab/_calls). Pipeline, job and trigger calls are answered 403 and recorded, so a test can show
none was made. Standard library only.

  python3 -I stubs.py [port]      # default 8099
"""
import json
import re
import sys
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

NOW = int(time.time())


def iso(ts):
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(ts))


GITLAB = {
    "/gitlab/api/v4/user": {"id": 1, "username": "argus-reader", "name": "Argus reader", "state": "active"},
    "/gitlab/api/v4/personal_access_tokens/self": {"id": 9, "name": "argus", "active": True, "revoked": False,
                                                    "scopes": ["read_api"], "expires_at": None},
    "/gitlab/api/v4/projects": [
        {"id": 101, "path_with_namespace": "shop/checkout", "default_branch": "main", "archived": False,
         "last_activity_at": iso(NOW - 3600)},
        {"id": 102, "path_with_namespace": "shop/catalog", "default_branch": "main", "archived": False,
         "last_activity_at": iso(NOW - 7200)},
    ],
    "/gitlab/api/v4/groups": [{"id": 5, "full_path": "shop", "name": "shop"}],
}
# the CI/CD watcher: shop/checkout has a CI file, a green develop pipeline and a failed release pipeline
GITLAB_CI = {
    "/gitlab/api/v4/projects/101/repository/branches": [
        {"name": b, "commit": {"id": "c0ffee%02d" % i, "committed_date": iso(NOW - 5400), "title": "feat: checkout step"}}
        for i, b in enumerate(("develop", "pre-release", "release"))],
    "/gitlab/api/v4/projects/101/protected_branches": [{"name": "release", "push_access_levels": [{"access_level": 40}],
                                                        "merge_access_levels": [{"access_level": 40}]}],
    "/gitlab/api/v4/projects/101/pipelines": [
        {"id": 7001, "ref": "develop", "status": "success", "sha": "c0ffee00", "source": "push",
         "created_at": iso(NOW - 3000), "updated_at": iso(NOW - 2700)},
        {"id": 7002, "ref": "release", "status": "failed", "sha": "c0ffee02", "source": "push",
         "created_at": iso(NOW - 2000), "updated_at": iso(NOW - 1800)}],
    "/gitlab/api/v4/projects/101/pipelines/7002/jobs": [
        {"id": 8002, "name": "build", "status": "failed", "stage": "build", "allow_failure": False,
         "failure_reason": "script_failure"}],
    "/gitlab/api/v4/projects/101/repository/commits/c0ffee02": {"author_name": "Dev One", "title": "feat: checkout step"},
    "/gitlab/api/v4/projects/101/repository/commits/c0ffee02/merge_requests": [{"iid": 12}],
}
GITLAB_RAW = {
    "/gitlab/api/v4/projects/101/repository/files/.gitlab-ci.yml/raw": "build:\n  script: make\n",
    "/gitlab/api/v4/projects/101/jobs/8002/trace": "npm ERR! Missing: left-pad@1.3.0 from lock file\n",
}
JIRA = {
    "/jira/rest/api/2/myself": {"name": "argus-bot", "displayName": "Argus bot", "active": True},
    "/jira/rest/api/2/serverInfo": {"version": "9.12.0", "deploymentType": "Server"},
    "/jira/rest/api/2/search": {"startAt": 0, "maxResults": 50, "total": 1, "issues": [
        {"key": "OPS-1", "fields": {"summary": "Rotate the test certificate", "status": {"name": "To Do"},
                                    "updated": iso(NOW - 600).replace("Z", ".000+0000")}}]},
}
ALERTS = [
    {"labels": {"alertname": "DiskAlmostFull", "severity": "warning", "instance": "app-01:9100", "job": "node"},
     "annotations": {"summary": "Disk 91 % full"}, "startsAt": iso(NOW - 1800), "status": {"state": "active"},
     "fingerprint": "a1b2c3d4"},
    {"labels": {"alertname": "TargetDown", "severity": "critical", "job": "checkout"},
     "annotations": {"summary": "checkout target down"}, "startsAt": iso(NOW - 300), "status": {"state": "active"},
     "fingerprint": "e5f6a7b8"},
]
PROM = {"status": "success", "data": {"alerts": [
    {"labels": a["labels"], "annotations": a["annotations"], "state": "firing", "activeAt": a["startsAt"]}
    for a in ALERTS]}}
MATRIX = {
    "/matrix/_matrix/client/versions": {"versions": ["v1.6"]},
    "/matrix/_matrix/client/v3/account/whoami": {"user_id": "@argus:chat.example.test"},
}


# --- the marketing module ------------------------------------------------------------------------------------------
GRAFANA = {
    "/grafana/api/user": {"login": "argus-viewer", "orgId": 1, "isGrafanaAdmin": False},
    "/grafana/api/user/orgs": [{"orgId": 1, "name": "Main", "role": "Viewer"}],
    "/grafana/api/frontend/settings": {"datasources": {"Logs": {"type": "loki", "uid": "loki-1"}}},
}
SMART = "Mozilla/5.0 (Linux; Android 14; Pixel 8) Mobile (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
# the edge lines as the job's LogQL formats them: ip, status, request time, bytes, host, upstream, cache, URI, user agent
LINES = ["66.249.66.1\t200\t0.120\t5120\twww.example.com\t10.0.0.5:8080\t-\t/product-1/blue-phone\t" + SMART,
         "66.249.66.2\t200\t0.300\t2048\twww.example.com\t10.0.0.5:8080\t-\t/category/phones?page=2\t" + SMART,
         "66.249.66.3\t503\t1.900\t0\twww.example.com\t10.0.0.6:8080\t-\t/checkout/start\t" + SMART,
         "198.51.100.7\t200\t0.050\t512\twww.example.com\t-\t-\t/\tGooglebot/2.1"]
GOOGLE_SITES = {"siteEntry": [{"siteUrl": "sc-domain:example.com", "permissionLevel": "siteRestrictedUser"}]}
SITEMAP_INDEX = "<sitemapindex><sitemap><loc>http://stubs:8099/site/sitemap-pages.xml</loc></sitemap></sitemapindex>"
SITEMAP = ("<urlset><url><loc>https://www.example.com/product-1/blue-phone</loc><lastmod>2026-11-01</lastmod></url>"
           "<url><loc>https://www.example.com/category/phones</loc></url></urlset>")
RANGES = {"creationTime": "%s" % time.strftime("%Y-%m-%dT00:00:00", time.gmtime()), "prefixes": [{"ipv4Prefix": "66.249.64.0/19"}]}
SEARCH = {"opensearch": {"version": {"number": "2.15.0", "distribution": "opensearch"}, "cluster_name": "logs"},
          "authinfo": {"user_name": "cdn-reader", "roles": ["readall"]}}


def loki(q):
    """query_range: the synthetic lines, one second after the window start (every window has the same traffic)."""
    start = int(q.get("start", ["0"])[0]) // 10 ** 9
    return {"status": "success", "data": {"resultType": "streams", "result": [
        {"stream": {"hostname": "edge-01"}, "values": [[str((start + 1) * 10 ** 9), l] for l in LINES]}]}}


def search_result():
    combo = lambda st, n: {"key": ["www.example.com", st, "allow-bots", "allow", "-", st, "HIT"], "doc_count": n}
    return {"hits": {"total": {"value": 900}}, "aggregations": {"known": {"doc_count": 30}, "g": {"ua": {"doc_count": 40}, "ip": {
        "buckets": [{"key": "66.249.66.1", "kb": {"buckets": [{"key": "Google"}]}, "k": {"buckets": [combo("200", 25), combo("503", 2)]}},
                    {"key": "198.51.100.7", "kb": {"buckets": [{"key": "-"}]}, "k": {"buckets": [combo("403", 6)]}}]}}}}


def google_post(path, body):
    try:
        q = json.loads(body or b"{}")
    except ValueError:
        q = {}
    if path == "/google/token":
        return {"access_token": "fake-google-access-0001", "expires_in": 3600}
    if path.endswith("/searchAnalytics/query"):
        dims = ",".join(q.get("dimensions", []))
        if dims == "date":
            day = lambda d: time.strftime("%Y-%m-%d", time.gmtime(NOW - d * 86400))
            return {"rows": [{"keys": [day(d)], "clicks": 1200 + 10 * d, "impressions": 40000, "ctr": 0.03, "position": 7.5}
                             for d in range(3, 10)]}
        if dims == "query,page":
            return {"rows": [{"keys": ["blue phone", "https://www.example.com/product-1/blue-phone"], "clicks": 50,
                              "impressions": 10000, "ctr": 0.005, "position": 2.0}]}
        return {"rows": []}
    if path == "/google/v1/urlInspection/index:inspect":
        return {"inspectionResult": {"indexStatusResult": {"verdict": "PASS", "coverageState": "Submitted and indexed",
                                                           "lastCrawlTime": iso(NOW - 7200), "crawledAs": "MOBILE",
                                                           "robotsTxtState": "ALLOWED", "pageFetchState": "SUCCESSFUL"}}}
    if path.endswith(":runReport"):
        return {"rows": [{"dimensionValues": [{"value": "/landing"}],
                          "metricValues": [{"value": "1000"}, {"value": "0.8"}, {"value": "3"}]}]}
    return None


def model_answer(body):
    """A Messages API answer (the api model provider): a fixed, synthetic text; no model is called."""
    text = "OK stub model answer"
    if b"Task: task-intake" in body:  # the task agent's planning run: a fixed, valid plan
        text = json.dumps({"classification": {"environment": "develop", "kind": "investigate"},
                           "summary": "Stub plan for the clean-install test.",
                           "plan": [{"text": "Read the task and report", "mode": "read-only", "targets": []}],
                           "questions": [], "needs": [], "estimate": {"effort": "5 min", "cost_usd": 0.01},
                           "confidence": "low", "suggested_column": "Done"})
    return {"type": "message", "role": "assistant", "model": "stub", "stop_reason": "end_turn",
            "content": [{"type": "text", "text": text}],
            "usage": {"input_tokens": len(body) // 4, "output_tokens": 5}}


# GitLab project CI/CD variables (ADR-0022 PR 3): "<project id>/<key>" -> variable; scope develop only
GL_VARS = {}
GL_CALLS = []
GL_PROJECTS = {101: "shop/checkout", 102: "shop/catalog"}
GL_VAR_RE = re.compile(r"^/gitlab/api/v4/projects/([0-9]+)/variables(?:/([A-Za-z0-9_]+))?$")
GL_DENIED = re.compile(r"/(pipeline|pipelines|jobs|trigger|triggers|deployments)(/|$)")


def gl_var_json(key, v):
    return {"key": key, "value": v["value"], "variable_type": v.get("variable_type", "env_var"), "protected": v.get("protected", False),
            "masked": v.get("masked", False), "raw": v.get("raw", False), "environment_scope": "develop", "hidden": False}


def gitlab_vars(method, raw_path, query, body):
    """The variables API of the stub GitLab: (code, body) or None when the path is not a variables call."""
    path = raw_path.rstrip("/")
    if not path.startswith("/gitlab/api/v4/projects/"):
        return None
    if method != "GET" and GL_DENIED.search(path):  # reads of pipelines and jobs are the CI collectors' and pass on
        GL_CALLS.append({"method": method, "path": path})
        return 403, {"message": "the stub refuses to start pipelines and jobs: Argus never starts them"}
    rest = path[len("/gitlab/api/v4/projects/"):]
    if "/" not in rest and method == "GET":  # a project lookup by id or URL-encoded path
        ref = urllib.parse.unquote(rest).lower()
        for pid, p in GL_PROJECTS.items():
            if ref in (str(pid), p):
                GL_CALLS.append({"method": method, "path": path})
                return 200, {"id": pid, "path_with_namespace": p}
        return None
    m = GL_VAR_RE.match(path)
    if not m:
        return None
    GL_CALLS.append({"method": method, "path": path})
    pid, key = int(m.group(1)), m.group(2)
    try:
        data = json.loads(body or b"{}")
    except ValueError:
        data = {}
    if key is None:
        if method != "POST":
            return 405, {"message": "method"}
        key = data.get("key", "")
        if data.get("environment_scope") != "develop" or not re.match(r"^[A-Za-z0-9_]{1,255}$", key):
            return 400, {"message": "bad request"}
        if "%d/%s" % (pid, key) in GL_VARS:
            return 400, {"message": "key has already been taken"}
        GL_VARS["%d/%s" % (pid, key)] = {k: data[k] for k in ("value", "variable_type", "protected", "masked", "raw") if k in data}
        return 201, gl_var_json(key, GL_VARS["%d/%s" % (pid, key)])
    if query.get("filter[environment_scope]") != ["develop"]:
        return 409, {"message": "the scope filter is missing"}
    cur = GL_VARS.get("%d/%s" % (pid, key))
    if cur is None:
        return 404, {"message": "404 Variable Not Found"}
    if method == "GET":
        return 200, gl_var_json(key, cur)
    if method == "PUT":
        cur.update({k: data[k] for k in ("value", "variable_type", "protected", "masked", "raw") if k in data})
        return 200, gl_var_json(key, cur)
    if method == "DELETE":
        del GL_VARS["%d/%s" % (pid, key)]
        return 204, None
    return 405, {"message": "method"}


class Handler(BaseHTTPRequestHandler):
    server_version = "stubs/1"

    def log_message(self, fmt, *args):
        sys.stderr.write("stubs %s %s\n" % (self.command, self.path))

    def send(self, code, body):
        if body is None:
            self.send_response(code)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def text(self, body, ctype="application/xml"):
        data = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path = self.path.split("?", 1)[0].rstrip("/") or "/"
        query = urllib.parse.parse_qs(self.path.split("?", 1)[1]) if "?" in self.path else {}
        if path == "/healthz":
            return self.send(200, {"ok": True})
        if path == "/gitlab/_calls":
            return self.send(200, GL_CALLS)
        out = gitlab_vars("GET", path, query, b"")
        if out is not None:
            return self.send(*out)
        if path == "/grafana/api/datasources/proxy/uid/loki-1/loki/api/v1/query_range":
            return self.send(200, loki(query))
        if path == "/google/webmasters/v3/sites":
            return self.send(200, GOOGLE_SITES)
        if path.startswith("/google/webmasters/v3/sites/") and path.endswith("/sitemaps"):
            return self.send(200, {"sitemap": [{"path": "https://www.example.com/sitemap.xml", "lastDownloaded": iso(NOW - 86400),
                                                "errors": "0", "warnings": "1", "contents": [{"type": "web", "submitted": "2"}]}]})
        if path == "/site/sitemap.xml":
            return self.text(SITEMAP_INDEX)
        if path == "/site/sitemap-pages.xml":
            return self.text(SITEMAP)
        if path == "/ranges/common.json":
            return self.send(200, RANGES)
        if path == "/search":
            return self.send(200, SEARCH["opensearch"])
        if path == "/search/_plugins/_security/authinfo":
            return self.send(200, SEARCH["authinfo"])
        if path in GITLAB_RAW:
            data = GITLAB_RAW[path].encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            return self.wfile.write(data)
        for table in (GITLAB, GITLAB_CI, JIRA, MATRIX, GRAFANA):
            if path in table:
                return self.send(200, table[path])
        if path == "/alertmanager/api/v2/alerts":
            return self.send(200, ALERTS)
        if path == "/prometheus/api/v1/alerts":
            return self.send(200, PROM)
        if path.startswith("/gitlab/api/v4/projects/"):
            return self.send(200, [])
        if path.startswith("/jira/rest/api/2/issue/"):
            return self.send(200, JIRA["/jira/rest/api/2/search"]["issues"][0])
        return self.send(404, {"error": "not in the stub"})

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(min(n, 1 << 20))
        path = self.path.split("?", 1)[0]
        query = urllib.parse.parse_qs(self.path.split("?", 1)[1]) if "?" in self.path else {}
        out = gitlab_vars(self.command, path, query, body)
        if out is not None:
            return self.send(*out)
        if path.startswith("/google/"):
            out = google_post(path, body)
            return self.send(200, out) if out is not None else self.send(404, {"error": "not in the stub"})
        if path.startswith("/search/") and path.endswith("/_search"):
            return self.send(200, search_result())
        if path.startswith("/jira/rest/api/2/issue"):
            return self.send(201, {"id": "10001", "key": "OPS-2"})
        if path == "/anthropic/v1/messages":
            return self.send(200, model_answer(body))
        return self.send(200, {"ok": True})

    do_PUT = do_POST
    do_DELETE = do_POST


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8099
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
