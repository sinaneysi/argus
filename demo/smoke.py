"""End-to-end smoke of the Next frontend + Go API without JavaScript: sign in as the demo admin through the
server-action form, read /today and /review, try a Review act (expects the demo refusal), check headers.
usage: python3 -I demo/smoke.py BASE_URL   (development tool, stdlib only, not shipped)"""
import html, http.client, re, sys, urllib.parse, uuid, ssl

html_mod = html  # forms() has a parameter called html

BASE = sys.argv[1].rstrip("/")
u = urllib.parse.urlsplit(BASE)
jar = {}

def req(method, path, body=None, headers=None):
    path = path.split("#")[0]  # a browser never sends the fragment of a redirect
    h = dict(headers or {})
    if jar:
        h["Cookie"] = "; ".join(f"{k}={v}" for k, v in jar.items())
    conn = (http.client.HTTPSConnection(u.netloc, context=ssl.create_default_context()) if u.scheme == "https"
            else http.client.HTTPConnection(u.netloc))
    conn.request(method, path, body=body, headers=h)
    r = conn.getresponse()
    data = r.read().decode("utf-8", "replace")
    for k, v in r.getheaders():
        if k.lower() == "set-cookie":
            name, val = v.split(";")[0].split("=", 1)
            if "Max-Age=0" in v or val == "":
                jar.pop(name, None)
            else:
                jar[name] = val
    return r.status, dict((k.lower(), v) for k, v in r.getheaders()), data

def ok(cond, msg):
    print(("PASS " if cond else "FAIL ") + msg)
    if not cond:
        ok.failed = True
ok.failed = False

def forms(html):
    out = []
    for m in re.finditer(r"<form([^>]*)>(.*?)</form>", html, re.S):
        fields = {k: html_mod.unescape(v) for k, v in re.findall(r'<input[^>]*?name="([^"]+)"[^>]*?value="([^"]*)"', m.group(2))}  # as a browser sends it
        for n in re.findall(r'<input[^>]*?name="([^"]+)"', m.group(2)):
            fields.setdefault(n, "")
        out.append((m.group(1), fields, m.group(2)))
    return out

def post_form(path, fields, extra=None):
    f = dict(fields); f.update(extra or {})
    b = "----e2e" + uuid.uuid4().hex
    parts = []
    for k, v in f.items():
        parts.append(f'--{b}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n')
    body = ("".join(parts) + f"--{b}--\r\n").encode()
    return req("POST", path, body, {"Content-Type": f"multipart/form-data; boundary={b}", "Origin": BASE})

s, h, body = req("GET", "/login?next=/today")
ok(s == 200 and "/_next/" in body, f"/login served by Next ({s})")
csp = h.get("content-security-policy", "")
ok("nonce-" in csp and "frame-ancestors 'none'" in csp and "script-src 'self'" in csp, "CSP with nonce: " + csp[:90])
ok("x-powered-by" not in h, "no X-Powered-By")
nonce = re.search(r"'nonce-([^']+)'", csp)
ok(nonce is not None and f'nonce="{nonce.group(1)}"' in body, "scripts carry the nonce")
ok(not re.search(r"<script(?![^>]*nonce=)[^>]*>", body), "no script without nonce")
ok(not re.search(r"<[^>]+\son[a-z]+=", re.sub(r"<script.*?</script>", "", body, flags=re.S)), "no inline event handlers")
ok(not re.search(r'<[^>]+\sstyle="', body), "no inline style attributes")
ok("argus_csrf" in jar, "double-submit cookie set")
admin = [f for f in forms(body) if f[1].get("username") == "demo.admin"]
ok(len(admin) == 1, "demo admin form present")
s, h, body = post_form("/login", admin[0][1])
ok(s == 303 and h.get("location", "").endswith("/today"), f"sign-in redirects to /today ({s} {h.get('location')})")
ok("opsr_session" in jar, "session cookie set")
s, h, body = req("GET", "/today")
ok(s == 200 and "/_next/" in body and "Needs you" in body and "Plan for today" in body, f"/today rendered by Next ({s})")
ok("Data as of" in body, "freshness pill")
s, h, body = req("GET", "/review")
ok(s == 200 and "Review inbox" in body and 'class="q-row' in body, f"/review rendered by Next ({s})")
first = re.search(r'href="(/review\?sel=[^"#]+)', body)
ok(first is not None, "queue rows link to ?sel=")
s, h, body = req("GET", first.group(1).replace("&amp;", "&"))
ok(s == 200 and 'id="rv-panel"' in body, "panel renders")
acts = [f for f in forms(body) if "sel" in f[1] and f[1].get("action") not in ("", None)]
ok(len(acts) > 0, f"{len(acts)} act forms in the panel")
if acts:
    s, h, _ = post_form(first.group(1).replace("&amp;", "&").split("#")[0], acts[0][1], {"reason": "e2e"})
    loc = h.get("location", "")
    ok(s == 303, f"act answered 303 to {loc}")
    s, h, body = req("GET", loc or "/review")
    ok("Disabled in the demo" in body, "demo refusal shown after the act")
s, h, body = req("GET", "/api/v1/me")
ok(s == 200 and '"demo.admin"' in body, "API /me with the session")

# the Incidents section: served by Next from real demo data, writes refused, other Go pages still there
def next_page(path, *words):
    s, h, body = req("GET", path)
    csp = h.get("content-security-policy", "")
    n = re.search(r"'nonce-([^']+)'", csp)
    good = s == 200 and "/_next/" in body and n is not None and f'nonce="{n.group(1)}"' in body
    good = good and not re.search(r'<[^>]+\sstyle="', body) and all(w in body for w in words)
    ok(good, f"{path} served by Next with a nonce ({s})")
    return body

body = next_page("/today", "Active incidents", "Checks, last 48 h")
body = next_page("/incidents", "<h1>Incidents</h1>", "/incidents/view?id=")
iid = re.search(r"/incidents/view\?id=(\d+)", body).group(1)
ivw = next_page(f"/incidents/view?id={iid}", 'id="timeline"', 'id="record"')
next_page("/incidents/problems", "log-rca runs")
fnd = re.search(r'/incidents/problem\?run=(\d+)&amp;idx=(\d+)', req("GET", "/incidents/problems")[2])
next_page(f"/incidents/problem?run={fnd.group(1)}&idx={fnd.group(2)}", "Root cause analysis")
run = re.search(r"/incidents/run\?id=(\d+)", req("GET", "/incidents/problems")[2])
next_page(f"/incidents/run?id={run.group(1)}", "Alerts firing at that time")
next_page("/alerts", "<h1>Alerts</h1>")
body = next_page("/problems", "<h1>Problems</h1>", "/problems/view?id=")
next_page("/problems/view?id=" + re.search(r"/problems/view\?id=(\d+)", body).group(1), 'id="linked"')
body = next_page("/rca", "RCA library", "/rca/view?id=")
next_page("/rca/view?id=" + re.search(r"/rca/view\?id=(\d+)", body).group(1), 'id="fix-prompt"')
acks = [f for f in forms(ivw) if f[1].get("action") == "ack"]
ok(len(acks) == 1, "incident page offers Acknowledge")
if acks:
    s, h, _ = post_form(f"/incidents/view?id={iid}", acks[0][1])
    s, h, body = req("GET", h.get("location", f"/incidents/view?id={iid}"))
    ok("Disabled in the demo" in body, "an incident act shows the demo refusal")
tok = jar.get("argus_csrf", "")
for p in ("/api/v1/incidents/act", "/api/v1/incidents/deep", "/api/v1/problems/act"):
    s, h, body = req("POST", p, '{"id":"1","action":"ack"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body, f"POST {p} refused in the demo ({s})")

# the Infrastructure section: served by Next from real demo data, writes refused, downloads still on the API
body = next_page("/inventory", "<h1>Inventory</h1>", "/inventory/view?id=", "Scan now")
vid = re.search(r"/inventory/view\?id=(\d+)", body).group(1)
vmp = next_page(f"/inventory/view?id={vid}", 'id="services"', 'id="upstreams"', 'id="analysis"', 'id="vulns"', 'id="filesystems"', 'id="record"', 'class="mini"')
next_page("/inventory?q=app", "<h1>Inventory</h1>")
s, h, _ = req("GET", f"/inventory?go=1&q=" + urllib.parse.quote(re.search(r"<h1[^>]*>([^<]+)</h1>", vmp).group(1)))
ok(s in (303, 307) and "/inventory/view?id=" in h.get("location", ""), f"the search goes to the one matching VM ({s})")
s, h, body = req("GET", "/inventory.csv")
ok(s == 200 and "csv" in h.get("content-type", "") and "name" in body[:40], f"/inventory.csv is still the download ({s})")
next_page("/capacity", "<h1>Capacity</h1>", 'id="fs"', 'id="vms"')
next_page("/certs", "<h1>Certificates</h1>", "By certificate")
body = next_page("/backups", "<h1>Backups</h1>", 'id="coverage"', 'id="plans"', "/backups/set?name=")
sname = re.search(r"/backups/set\?name=([^\"#&]+)", body).group(1)
next_page("/backups/set?name=" + sname, "Size of the newest backup", "History")
next_page("/backups/plan?id=" + re.search(r"/backups/plan\?id=(\d+)", body).group(1), "Restore tests", "Job directory")
bnew = next_page("/backups/new", "<h1>New backup</h1>", 'type="password"', "Test connection")
chk = next_page("/operations", "<h1>Checks</h1>", 'id="checks"', 'class="strip"', "Argus health")
next_page("/hygiene", "<h1>Alert hygiene</h1>", "Suggestions only")
next_page("/seo", "<h1>SEO</h1>", "Operations summary", "Send to chat", "prod-edge-01", 'class="line-c"', "Index state of key URLs")
scans = [f for f in forms(chk) if f[1].get("tasks") == "all"]
ok(len(scans) == 1, "Checks offers Scan all")
if scans:
    s, h, _ = post_form("/operations", scans[0][1])
    s, h, body = req("GET", h.get("location", "/operations"))
    ok("Disabled in the demo" in body, "Scan all shows the demo refusal")
tests = [f for f in forms(bnew) if "uri" in f[1]]
ok(len(tests) == 1, "New backup has its form")
if tests:
    s, h, _ = post_form("/backups/new", tests[0][1], {"uri": "mongodb://u:" + "smoke-secret" + "@db-1:27017/", "action": "test"})
    s, h, body = req("GET", h.get("location", "/backups/new"))
    ok("Disabled in the demo" in body and "smoke-secret" not in body, "a backup test shows the demo refusal and not the string")
for p in ("/api/v1/scan", "/api/v1/inventory/refresh", "/api/v1/inventory/analyse", "/api/v1/capacity/analyse", "/api/v1/backups/new", "/api/v1/backups/restore", "/api/v1/backups/install", "/api/v1/backups/override", "/api/v1/seo/send"):
    s, h, body = req("POST", p, '{"id":"1"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body, f"POST {p} refused in the demo ({s})")
# the Delivery section: served by Next from real demo data, writes refused, downloads and summary still on the API
body = next_page("/cicd", "<h1>CI/CD</h1>", "Green by environment", 'id="matrix"', 'class="envbar"', "/cicd/project?id=")
pid = re.search(r"/cicd/project\?id=(\d+)", body).group(1)
next_page(f"/cicd/project?id={pid}", "Open findings", "Deploys", "Pipelines")
body = next_page("/cicd/failures", "<h1>CI/CD failures</h1>", "/cicd/finding?id=", "Failure views")
next_page("/cicd/finding?id=" + re.search(r"/cicd/finding\?id=(\d+)", body).group(1), "Proposed fix", "Evidence")
next_page("/cicd/stuck", "<h1>Stuck pipelines</h1>", "Release deploys waiting at manual")
next_page("/cicd/audit", "<h1>Branches and variables</h1>", 'id="variables"')
body = next_page("/cicd/repo", "<h1>Check a repo</h1>", "/cicd/repo?project=")
rp = next_page("/cicd/repo?project=" + re.search(r"/cicd/repo\?project=([^\"#&]+)", body).group(1), "Pipelines per environment")
checks = [f for f in forms(rp) if "project" in f[1] and "csrf" in f[1]]
ok(len(checks) == 1, "a repo report offers Re-check")
if checks:
    s, h, _ = post_form("/cicd/repo", checks[0][1])
    s, h, body = req("GET", h.get("location", "/cicd/repo"))
    ok("Disabled in the demo" in body, "Re-check shows the demo refusal")
body = next_page("/vulns", "<h1>Vulnerabilities</h1>", "Fix first", 'id="components"', 'id="coverage"')
gp = next_page(html.unescape(re.search(r'href="(/vulns\?group=[^"]+)"', body).group(1)), 'id="affected"', "Suppressions")
sup = [f for f in forms(gp) if "reason" in f[1]]
ok(len(sup) == 1, "the admin gets the suppress form")
if sup:
    s, h, _ = post_form("/vulns", sup[0][1], {"reason": "Smoke test of the demo refusal only"})
    s, h, body = req("GET", h.get("location", "/vulns"))
    ok("Disabled in the demo" in body, "Suppress shows the demo refusal")
for p in ("/api/v1/cicd/repo/check", "/api/v1/vulns/suppress", "/api/v1/account/stepup"):
    s, h, body = req("POST", p, '{"id":"1"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body, f"POST {p} refused in the demo ({s})")
s, h, body = req("GET", "/cicd/summary.json")
ok(s == 200 and "json" in h.get("content-type", ""), f"/cicd/summary.json is still the API ({s})")
s, h, body = req("GET", "/account/confirm?next=%2Fvulns")
ok(s in (200, 303, 307) and "content-security-policy" in h, f"the step-up page is served by Next ({s})")

# the Work section: board, task page and New task on Next; every write control shows the demo refusal
body = next_page("/board?show=all", "<h1>Tasks</h1>", "/board/task?id=", "Refresh now", "Only my issues", 'id="col-0"')
moves = [f for f in forms(body) if "key" in f[1] and "csrf" in f[1]]
ok(len(moves) > 0, "the admin gets Move on the cards")
if moves:
    s, h, _ = post_form("/board?show=all", moves[0][1], {"col": "0"})
    s, h, body2 = req("GET", h.get("location", "/board?show=all"))
    ok("Disabled in the demo" in body2, "Move shows the demo refusal")
tid = re.search(r"/board/task\?id=(\d+)", body).group(1)
tp = next_page("/board/task?id=" + tid, 'id="comments"', "Record", "History")
cm = [f for f in forms(tp) if "body" in f[1] or 'name="body"' in f[2]]
ok(len(cm) == 1, "a task offers the comment form")
if cm:
    s, h, _ = post_form("/board/task?id=" + tid, cm[0][1], {"body": "Smoke test of the demo refusal only"})
    s, h, body2 = req("GET", h.get("location", "/board/task?id=" + tid))
    ok("Disabled in the demo" in body2, "Comment shows the demo refusal")
agent = next((m for m in re.findall(r"/board/task\?id=(\d+)", body) if "Argus works this task" in req("GET", "/board/task?id=" + m)[2]), None)
ok(agent is not None, "a task shows the agent panel")
if agent:
    next_page("/board/task?id=" + agent, "Argus works this task", 'id="chat"', "Spin up")
nt = next_page("/board/new", "<h1>New task</h1>", 'name="title"', 'name="fix_prompt"')
tf = [f for f in forms(nt) if "title" in f[1]]
if tf:
    s, h, body2 = post_form("/board/new", tf[0][1], {"title": "Smoke test", "priority": "Medium"})
    ok("Disabled in the demo" in body2 and "Smoke test" in body2, f"New task keeps the typed title on the demo refusal ({s})")
for p in ("/api/v1/board/create", "/api/v1/board/comment", "/api/v1/board/move", "/api/v1/board/assign", "/api/v1/board/fixer", "/api/v1/board/jmove",
          "/api/v1/board/refresh", "/api/v1/board/agent", "/api/v1/board/agent/spinup", "/api/v1/board/agent/chat", "/api/v1/board/task/secret", "/api/v1/board/mxpreview"):
    s, h, body2 = req("POST", p, '{"task":"1"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body2, f"POST {p} refused in the demo ({s})")
s, h, body2 = req("POST", "/board/move", "task=1&to=done", {"Content-Type": "application/x-www-form-urlencoded", "Origin": BASE})
ok(s in (404, 405), f"the old POST /board/move is gone ({s})")

body = next_page("/qa?state=all", "<h1>Conversations</h1>", 'id="issue-', 'id="pblink"', "Open issues")
next_page("/qa?view=messages", 'name="from"', "Actions taken", "<h1>Conversations</h1>")
next_page("/qa?disp=problem", "Actions taken")
ak = next_page("/board/ask", "<h1>Ask Argus</h1>", 'name="text"', "Recent threads")
thread = re.search(r"/board/ask\?key=(new-[0-9a-f]{8})", ak)
ok(thread is not None, "Ask Argus lists a thread")
if thread:
    next_page("/board/ask?key=" + thread.group(1), "Chat with Argus", "All threads")
cf = [f for f in forms(ak) if "text" in f[2] and "key" in f[1]]
ok(len(cf) == 1, "Ask Argus offers the composer")
if cf:
    s, h, _ = post_form("/board/ask", cf[0][1], {"text": "Smoke test of the demo refusal only"})
    s, h, body2 = req("GET", h.get("location", "/board/ask"))
    ok("Disabled in the demo" in body2, "Send shows the demo refusal")

# the Agent section: served by Next from real demo data, the fix request refused, other Go pages still there
body = next_page("/runs", "<h1>Agent runs</h1>", "Cost per day", "Show data", 'name="task"', "/runs/view?id=")
next_page("/runs?task=chat-triage&sort=oldest", "chat-triage")
ok("/runs/view?id=" in body, "Runs links to a run")
chat = next_page("/qa?view=messages")
rid = next((m for m in re.findall(r"/runs/view\?id=(\d+)", chat)), None)
ok(rid is not None, "the /qa chain links to a run")
if rid:
    next_page("/runs/view?id=" + rid, "Summary", "Messages handled", "Record", "Agent runs")
next_page("/knowledge", "What Argus knows", "About this page", "Review lessons", "<h3")
fl = next_page("/admin/fix", "<h1>Fix runs</h1>", "Argus never fixes production", "/admin/fix/run?id=")
fr = re.search(r"/admin/fix/run\?id=([0-9a-f-]+)", fl)
ok(fr is not None, "Fix runs links to a run")
if fr:
    next_page("/admin/fix/run?id=" + fr.group(1), "Rollback commands", "Verification", "Final report", "Exact prompt the fixer got")
pr = None
for r_ in re.findall(r"/incidents/problem\?run=(\d+)&amp;idx=(\d+)", req("GET", "/incidents?env=pre-release&state=all")[2] + req("GET", "/incidents?env=develop&state=all")[2]):
    if 'name="prompt"' in req("GET", f"/admin/fix/review?run={r_[0]}&idx={r_[1]}")[2]:
        pr = r_
        break
if pr is None:
    for n in range(1150, 1260):
        if 'name="prompt"' in req("GET", f"/admin/fix/review?run={n}&idx=0")[2]:
            pr = (str(n), "0")
            break
ok(pr is not None, "a finding off production offers Take action")
if pr:
    rv = next_page(f"/admin/fix/review?run={pr[0]}&idx={pr[1]}", "<h1>Take action</h1>", 'name="prompt"', 'name="host"', 'name="reviewed"', "Run fix")
    ff = [f for f in forms(rv) if "prompt" in f[2]]
    ok(len(ff) == 1, "Take action offers the form")
    if ff:
        s, h, body2 = post_form(f"/admin/fix/review?run={pr[0]}&idx={pr[1]}", ff[0][1], {"prompt": "Smoke test of the demo refusal only", "host": "x", "reviewed": "1"})
        ok("Disabled in the demo" in body2 and "Smoke test of the demo refusal only" in body2, f"Run fix keeps the prompt on the demo refusal ({s})")
s, h, body2 = req("POST", "/api/v1/fix/review", '{"run":"1","idx":"0"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
ok(s == 403 and "Disabled in the demo" in body2, f"POST /api/v1/fix/review refused in the demo ({s})")
s, h, body2 = req("POST", "/admin/fix/review", "run=1&idx=0", {"Content-Type": "application/x-www-form-urlencoded", "Origin": BASE})
ok(s in (200, 403, 404), f"the old POST /admin/fix/review changes nothing ({s})")
# Review's full lists of ideas and proposed tasks: Next, with real demo data; the other kinds are still the Go page
ib = next_page("/review?kind=ideas&mode=full&status=all", "<h1>Ideas</h1>", "Advisers", "Generate now", "Mark under review", 'name="persona"')
ai = [f for f in forms(ib) if f[1].get("action") == "explore"]
ok(len(ai) > 0, "Ideas offers Mark under review")
if ai:
    s, h, _ = post_form("/review?kind=ideas&mode=full&status=all", ai[0][1])
    s, h, body2 = req("GET", h.get("location", "/review?kind=ideas&mode=full"))
    ok("Disabled in the demo" in body2, "An idea act shows the demo refusal")
pb = next_page("/review?kind=proposals&mode=full", "<h1>Proposed tasks</h1>", 'id="pbulk"', 'name="assignee"', "Approval settings", 'name="expiry_days"')
next_page("/review?kind=proposals&mode=full&sev=critical&sev=high", "<h1>Proposed tasks</h1>", "sev=critical&amp;sev=high")
next_page("/review?kind=proposals&mode=full&view=decided", "Decided in the last 14 days", "Outcome")
pid = re.search(r'id="p-(\d+)"', pb)
ok(pid is not None, "Proposed tasks lists a row")
if pid:
    cb = next_page(f"/review?kind=proposals&mode=full&confirm=reject&id={pid.group(1)}", "Reject 1 proposed task?", "Cancel")
    cf = [f for f in forms(cb) if f[1].get("action") == "reject"]
    ok(len(cf) == 1, "the confirm step offers one form")
    if cf:
        s, h, _ = post_form(f"/review?kind=proposals&mode=full&confirm=reject&id={pid.group(1)}", cf[0][1])
        s, h, body2 = req("GET", h.get("location", "/review?kind=proposals&mode=full"))
        ok("Disabled in the demo" in body2, "A proposal act shows the demo refusal")
for p in ("/api/v1/ideas/act", "/api/v1/ideas/generate", "/api/v1/proposals/act", "/api/v1/proposals/settings"):
    s, h, body2 = req("POST", p, '{"id":"1"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body2, f"POST {p} refused in the demo ({s})")
# Review's full lists of lessons, links and chat requests: Next, the demo refuses every decision
lb = next_page("/review?kind=lessons&mode=full", "<h1>Lessons</h1>", "Edit and approve", 'name="lkind"', "Save setting", "Save and approve")
next_page("/review?kind=lessons&mode=full&view=approved", "Back to review", "Retire")
next_page("/review?kind=lessons&mode=full&view=approved&sort=used", "Most used")
la = [f for f in forms(lb) if f[1].get("action") == "approve"]
ok(len(la) > 0, "Lessons offers Approve")
if la:
    s, h, _ = post_form("/review?kind=lessons&mode=full", la[0][1])
    s, h, body2 = req("GET", h.get("location", "/review?kind=lessons&mode=full"))
    ok("Disabled in the demo" in body2, "A lesson act shows the demo refusal")
sb = next_page("/review?kind=links&mode=full", "<h1>Suggestions</h1>", "Accept link", 'id="pbsug"', "Accept links…", 'name="conf"')
lid = re.search(r'id="s-(\d+)"', sb)
ok(lid is not None, "Suggestions lists a row")
if lid:
    cb = next_page(f"/review?kind=links&mode=full&confirm=reject&link={lid.group(1)}", "Reject 1 suggested link?", "Cancel")
    cf = [f for f in forms(cb) if f[1].get("action") == "reject"]
    ok(len(cf) == 1, "the confirm step offers one form")
    if cf:
        s, h, _ = post_form(f"/review?kind=links&mode=full&confirm=reject&link={lid.group(1)}", cf[0][1])
        s, h, body2 = req("GET", h.get("location", "/review?kind=links&mode=full"))
        ok("Disabled in the demo" in body2, "A link decision shows the demo refusal")
rb = next_page("/review?kind=replies&mode=full", "<h1>Chat requests</h1>", 'name="assignee"', "Unassigned")
for p in ("/api/v1/lessons/act", "/api/v1/lessons/settings"):
    s, h, body2 = req("POST", p, '{"id":"1"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body2, f"POST {p} refused in the demo ({s})")
for p, word in (("/proposals", "kind=proposals"), ("/ideas", "kind=ideas")):
    s, h, body = req("GET", p)
    ok(s == 301 and word in h.get("location", ""), f"{p} goes to Review ({s})")
# Help: every page from the files, folds, admin blocks for the admin, search; no writes here
hb = next_page("/help", "<h1>Help</h1>", 'action="/help"', "h-cards", 'href="/help/settings"')
next_page("/help?q=backup", "<mark>", "Clear the search")
next_page("/help?q=zzzqqq", "Nothing found for")
for slug in re.findall(r'<h2><a href="/help/([a-z0-9-]+)"', hb):
    next_page("/help/" + slug, "On this page", "Help pages", "h-doc")
next_page("/help/settings", "Admins", "h-sec")
next_page("/help/all", "Argus help, all pages", 'id="p-review"', "<details")
s, h, body = req("GET", "/help/nope")
ok(s == 404 and "/_next/" in body, f"an unknown help page is not found ({s})")
# Settings: General and Users and access on Next; a save, the Claude switch and a user action are refused in the demo
gb = next_page("/admin", "<h1>General</h1>", "Save and apply", "Claude usage", "values are never shown", 'name="auth:sso_only"')
gf = [f for f in forms(gb) if "rd:present" in f[2]]
ok(len(gf) == 1, "General offers one settings form")
if gf:
    s, h, body2 = post_form("/admin", gf[0][1])
    ok("Disabled in the demo" in body2, f"Save and apply shows the demo refusal ({s})")
ub = next_page("/admin/users", "<h1>Users and access</h1>", "(you)", "Sign out everywhere", "Add user", 'name="matrix_id"')
uf = [f for f in forms(ub) if f[1].get("action") == "add"]
ok(len(uf) == 1, "Users offers Add user")
if uf:
    s, h, body2 = post_form("/admin/users", uf[0][1], {"user": "smoke.test"})
    ok("Disabled in the demo" in body2 and "shown only now" not in body2, f"Add user shows the demo refusal ({s})")
for p in ("/api/v1/admin/save", "/api/v1/admin/claude", "/api/v1/admin/users/act", "/api/v1/admin/users/matrix"):
    s, h, body2 = req("POST", p, '{"user":"x"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body2, f"POST {p} refused in the demo ({s})")
# Settings: Jira sync on Next; its writes are refused in the demo, no token value on the page
jb = next_page("/admin/board", "<h1>Jira sync</h1>", "Sync now", "Full reconcile", "Personal access token", 'name="qa_cap"', "Allowed Jira hosts", "Last good sync", 'class="help-q"')
hf = [f for f in forms(jb) if 'name="hosts"' in f[2]]
ok(len(hf) == 1, "Jira sync offers the hosts form")
if hf:
    s, h, body2 = post_form("/admin/board", hf[0][1], {"hosts": "jira.northwind.example"})
    ok("Disabled in the demo" in body2, f"Save hosts shows the demo refusal ({s})")
sf = [f for f in forms(jb) if "Sync now" in f[2]]
ok(len(sf) == 1, "Jira sync offers Sync now")
if sf:
    s, h, _ = post_form("/admin/board", sf[0][1], {"action": "sync"})
    s, h, body2 = req("GET", h.get("location", "/admin/board"))
    ok("Disabled in the demo" in body2, "Sync now shows the demo refusal")
for p in ("/api/v1/admin/board", "/api/v1/admin/jira-hosts", "/api/v1/admin/jira-shared", "/api/v1/admin/agent", "/api/v1/admin/agent/fixer-move"):
    s, h, body2 = req("POST", p, '{"action":"sync"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body2, f"POST {p} refused in the demo ({s})")
s, h, body2 = req("GET", "/incidents")
ok('class="help-q"' in body2 and "/help/operations#incidents" in body2, "the ? beside the title goes to the page's help")
# Settings: Claude on Next; a test, a pasted token and the guard are refused in the demo
cb = next_page("/admin/claude", "<h1>Claude</h1>", 'id="claude-now"', "Test current token", "Replace and test", "Check now", "Guard settings", 'name="blocked"', "Claude usage")
tf = [f for f in forms(cb) if "Test current token" in f[2]]
ok(len(tf) == 1, "Claude offers Test current token")
if tf:
    s, h, _ = post_form("/admin/claude", tf[0][1], {"action": "test"})
    s, h, body2 = req("GET", h.get("location", "/admin/claude"))
    ok("Disabled in the demo" in body2, "Test current token shows the demo refusal")
gf = [f for f in forms(cb) if 'name="blocked"' in f[2]]
ok(len(gf) == 1, "Claude offers the guard settings")
if gf:
    s, h, body2 = post_form("/admin/claude", gf[0][1])
    ok("Disabled in the demo" in body2, f"Save guard settings shows the demo refusal ({s})")
for p in ("/api/v1/admin/claude-token", "/api/v1/admin/claude-egress"):
    s, h, body2 = req("POST", p, '{"action":"test"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body2, f"POST {p} refused in the demo ({s})")
# Settings: Kubernetes on Next; a check and a save are refused in the demo, the form never shows a kubeconfig
kb = next_page("/admin/kube", "<h1>Kubernetes</h1>", 'id="kube-now"', "Check again", "Replace…", "Add a cluster", 'name="kubeconfig"', "argus-ro.yaml")
kf = [f for f in forms(kb) if 'name="kubeconfig"' in f[2]]
ok(len(kf) == 1, "Kubernetes offers the add form")
if kf:
    s, h, body2 = post_form("/admin/kube", kf[0][1], {"name": "smoke", "env": "develop"})
    ok("Disabled in the demo" in body2, f"Save and check shows the demo refusal ({s})")
cf = [f for f in forms(kb) if "Check again" in f[2]]
ok(len(cf) > 0, "Kubernetes offers Check again")
if cf:
    s, h, _ = post_form("/admin/kube", cf[0][1])
    s, h, body2 = req("GET", h.get("location", "/admin/kube"))
    ok("Disabled in the demo" in body2, "Check again shows the demo refusal")
s, h, body2 = req("POST", "/api/v1/admin/kube", '{"action":"test","name":"dev"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
ok(s == 403 and "Disabled in the demo" in body2, f"POST /api/v1/admin/kube refused in the demo ({s})")
# Settings: Data sources on Next; add, check and save are refused in the demo, no credential value on the page
sb2 = next_page("/admin/sources", "<h1>Data sources</h1>", 'id="src-now"', "Add data source", "Check all now", "Edit…", "credential ends")
next_page("/admin/sources?add=1", "Add a data source", "Coming next")
ab = next_page("/admin/sources?add=grafana", "Add Grafana", 'name="credential"', "Save and check")
af = [f for f in forms(ab) if 'name="credential"' in f[2]]
ok(len(af) == 1, "Data sources offers the add form")
if af:
    s, h, body2 = post_form("/admin/sources?add=grafana", af[0][1], {"name": "smoke", "env": "develop", "url": "https://g.northwind.example"})
    ok("Disabled in the demo" in body2, f"Save and check shows the demo refusal ({s})")
cf = [f for f in forms(sb2) if f[1].get("action") == "check-all"]
ok(len(cf) == 1, "Data sources offers Check all now")
if cf:
    s, h, _ = post_form("/admin/sources", cf[0][1])
    s, h, body2 = req("GET", h.get("location", "/admin/sources"))
    ok("Disabled in the demo" in body2, "Check all now shows the demo refusal")
s, h, body2 = req("POST", "/api/v1/admin/sources", '{"action":"check-all"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
ok(s == 403 and "Disabled in the demo" in body2, f"POST /api/v1/admin/sources refused in the demo ({s})")
tg = next_page("/admin/backup-targets", "<h1>Backup targets</h1>", "backup-east", "Test target", "ILM", "Add a target", 'name="secret_key"', 'name="envs"', "Add and test")
tf = [f for f in forms(tg) if "Test target" in f[2]]
ok(len(tf) > 0, "Backup targets offers Test target")
if tf:
    s, h, _ = post_form("/admin/backup-targets", tf[0][1])
    s, h, body2 = req("GET", h.get("location", "/admin/backup-targets"))
    ok("Disabled in the demo" in body2, "Test target shows the demo refusal")
s, h, body2 = req("POST", "/api/v1/admin/backup-targets", '{"action":"test","name":"backup-east"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
ok(s == 403 and "Disabled in the demo" in body2, f"POST /api/v1/admin/backup-targets refused in the demo ({s})")
next_page("/admin/credentials", "<h1>Credentials</h1>", "Need attention", "GitLab read-only", "How to rotate", "Check now", 'value="cred-expiry"', "Values are never read by this app")
s, h, body2 = req("POST", "/api/v1/scan", '{"tasks":"cred-expiry","back":"/admin/credentials"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
ok(s == 403 and "Disabled in the demo" in body2, f"Check now refused in the demo ({s})")
ol = next_page("/admin/outline", "<h1>Integrations</h1>", "Connected as", "shop/checkout", "Save mode and scope", 'id="gitlab"', "Default project for new issues", "Save defaults", 'name="who"')
sf = [f for f in forms(ol) if "Sync now: Outline" in f[2]]
ok(len(sf) > 0, "Integrations offers Sync now")
if sf:
    s, h, _ = post_form("/admin/outline", sf[0][1])
    s, h, body2 = req("GET", h.get("location", "/admin/outline"))
    ok("Disabled in the demo" in body2, "Sync now shows the demo refusal")
next_page("/admin/outline/page?s=shop/checkout", "<h1>shop/checkout</h1>", "Secret scan", "Changes since the previous build", "Page as Outline will show it", "Markdown sent to Outline", "Owned by the shop team")
for p in ("/api/v1/admin/outline", "/api/v1/admin/gitlab-issues"):
    s, h, body2 = req("POST", p, '{"action":"sync"}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body2, f"POST {p} refused in the demo ({s})")
cg = next_page("/admin/sources/catalogue", "<h1>Catalogue</h1>", "Route decisions", "Routes by type", "Discover now")
rf = [f for f in forms(cg) if "Discover now" in f[2]]
if rf:
    s, h, _ = post_form("/admin/sources/catalogue", rf[0][1])
    s, h, body2 = req("GET", h.get("location", "/admin/sources/catalogue"))
    ok("Disabled in the demo" in body2, "Discover now shows the demo refusal")
s, h, body2 = req("POST", "/api/v1/sources/review/act", '{"action":"confirm","id":1}', {"Content-Type": "application/json", "X-CSRF-Token": tok})
ok(s == 403 and "Disabled in the demo" in body2, f"POST /api/v1/sources/review/act refused in the demo ({s})")
ac = next_page("/account", "<h1>Account</h1>", "Change password", "Two-factor authentication", "Your sessions", 'id="jira"', 'id="matrix"', 'name="who"')
next_page("/account/2fa", "<h1>Two-factor authentication</h1>", "Turn on two-factor authentication")
for p, b in (("/api/v1/account", '{"current":"x","new":"y","new2":"y"}'), ("/api/v1/account/jira", '{"action":"disconnect"}'), ("/api/v1/account/matrix", '{"matrix_id":""}'),
             ("/api/v1/account/sessions", '{"action":"others"}'), ("/api/v1/account/2fa/begin", '{"password":"x"}'), ("/api/v1/account/2fa/confirm", '{"code":"000000"}'),
             ("/api/v1/account/2fa/disable", '{"code":"000000"}'), ("/api/v1/account/2fa/recovery", '{"code":"000000"}')):
    s, h, body2 = req("POST", p, b, {"Content-Type": "application/json", "X-CSRF-Token": tok})
    ok(s == 403 and "Disabled in the demo" in body2, f"POST {p} refused in the demo ({s})")
s, h, _ = req("GET", "/account/2fa")
ok("no-store" in h.get("cache-control", ""), f"/account/2fa is no-store ({h.get('cache-control', '')})")
s, h, body2 = req("GET", "/api/v1/account/2fa")
ok(s == 200 and "otpauth" not in body2 and "no-store" in h.get("cache-control", ""), f"/api/v1/account/2fa holds no seed and is no-store ({s})")
# Go renders no page: an unknown page is the web app's 404, an unknown API path is JSON, the API paths stay routed
s, h, body = req("GET", "/no-such-page")
ok(s == 404 and "/_next/" in body, f"an unknown page is the web app's 404 ({s})")
s, h, body = req("GET", "/api/v1/no-such-endpoint")
ok(s == 404 and "json" in h.get("content-type", ""), f"an unknown API path is JSON ({s})")
s, h, body = req("GET", "/static/favicon.svg")
ok(s == 200 and "svg" in h.get("content-type", ""), f"/static is still the API ({s})")
s, h, body = req("GET", "/static/style.css")
ok(s == 404, f"the Go page CSS is gone ({s})")
s, h, body = req("GET", "/auth/login")
ok(s == 404 and "json" in h.get("content-type", ""), f"/auth is still the API: no single sign-on in the demo ({s})")
sys.exit(1 if ok.failed else 0)
