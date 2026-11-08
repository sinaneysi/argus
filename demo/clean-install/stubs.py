#!/usr/bin/env python3
"""Fake data sources for the clean-install test: GitLab, Jira, Alertmanager and Matrix behind one HTTP port, each
under its own path prefix. Synthetic data only; every write is answered and forgotten. Standard library only.

  python3 -I stubs.py [port]      # default 8099
"""
import json
import sys
import time
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


def model_answer(body):
    """A Messages API answer (the api model provider): a fixed, synthetic text; no model is called."""
    return {"type": "message", "role": "assistant", "model": "stub", "stop_reason": "end_turn",
            "content": [{"type": "text", "text": "OK stub model answer"}],
            "usage": {"input_tokens": len(body) // 4, "output_tokens": 5}}


class Handler(BaseHTTPRequestHandler):
    server_version = "stubs/1"

    def log_message(self, fmt, *args):
        sys.stderr.write("stubs %s %s\n" % (self.command, self.path))

    def send(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path = self.path.split("?", 1)[0].rstrip("/") or "/"
        if path == "/healthz":
            return self.send(200, {"ok": True})
        if path in GITLAB_RAW:
            data = GITLAB_RAW[path].encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            return self.wfile.write(data)
        for table in (GITLAB, GITLAB_CI, JIRA, MATRIX):
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
        if path.startswith("/jira/rest/api/2/issue"):
            return self.send(201, {"id": "10001", "key": "OPS-2"})
        if path == "/anthropic/v1/messages":
            return self.send(200, model_answer(body))
        return self.send(200, {"ok": True})

    do_PUT = do_POST


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8099
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
