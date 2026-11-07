package app

// Port of app/views_repocheck.py: CI/CD -> Check a repo. Data: ops.db cicd_readiness (bin/cicd_repo_check.py writes it;
// the app never calls GitLab and never runs the analysis).

import (
	"os"
	"path/filepath"
	"regexp"

	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var (
	repoGitlabURL = strings.TrimRight(GITLAB_URL, "/")
	repoSeg       = `[A-Za-z0-9][A-Za-z0-9._-]{0,99}`
	repoPathRE    = regexp.MustCompile(`^` + repoSeg + `(?:/` + repoSeg + `){1,5}$`)
	repoIDRE      = regexp.MustCompile(`^[0-9]{1,7}$`)
	repoGitRE     = regexp.MustCompile(`^(?:ssh://)?git@([A-Za-z0-9.-]+)[:/](.+)$`)
	repoSchemeRE  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
	repoBareRE    = regexp.MustCompile(`^[A-Za-z0-9.-]+\.[A-Za-z]{2,}/`)
	repoVerdict   = map[string][2]string{"ready": {"ok", "Ready"}, "warn": {"warn", "Ready with warnings"},
		"not_ready": {"fail", "Not ready"}, "error": {"warn", "Not checked"}}
	repoShort  = map[string]string{"ready": "Ready", "warn": "Warn", "not_ready": "Not ready", "error": "Error"}
	repoStatus = map[string][2]string{"fail": {"fail", "fail"}, "unknown": {"warn", "unknown"}, "pass": {"ok", "pass"},
		"na": {"ok", "n/a"}}
)

const (
	repoGap      = 60
	repoRunningS = 300
)

func repoVerdictOf(v string) [2]string {
	if x, ok := repoVerdict[v]; ok {
		return x
	}
	return [2]string{"warn", v}
}

// repoHostOf is urlsplit(url).hostname (lower-cased, "" for none) for a URL without brackets.
func repoHostOf(netloc string) string {
	if i := strings.LastIndex(netloc, "@"); i >= 0 {
		netloc = netloc[i+1:]
	}
	if i := strings.Index(netloc, ":"); i >= 0 {
		netloc = netloc[:i]
	}
	return strings.ToLower(netloc)
}

func repoGitlabHost() string {
	u := repoGitlabURL
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return repoHostOf(u)
}

func repoASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// RepoParseTarget is parse_target(text): (value, error message).
func RepoParseTarget(text string) (string, string) {
	s := py.Strip(text)
	if s == "" {
		return "", "Paste a GitLab project link, a group/project path or a project id."
	}
	s = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(s)
	if py.Len(s) > 300 {
		return "", "That is too long for a project link."
	}
	host := repoGitlabHost()
	if repoIDRE.MatchString(s) {
		return s, ""
	}
	var h, rest string
	if m := repoGitRE.FindStringSubmatch(s); m != nil {
		h, rest = strings.ToLower(m[1]), m[2]
	} else if repoSchemeRE.MatchString(s) {
		i := strings.Index(s, "://")
		scheme, after := strings.ToLower(s[:i]), s[i+3:]
		netloc, tail := after, ""
		if j := strings.IndexAny(after, "/?#"); j >= 0 {
			netloc, tail = after[:j], after[j:]
		}
		if !repoASCII(netloc) || strings.ContainsAny(netloc, "[]") {
			return "", "That is not a GitLab project link."
		}
		if scheme != "http" && scheme != "https" {
			return "", "Only https links to the company GitLab are accepted."
		}
		if i := strings.LastIndex(netloc, "@"); i >= 0 {
			userinfo := netloc[:i]
			user, pass := userinfo, ""
			if k := strings.Index(userinfo, ":"); k >= 0 {
				user, pass = userinfo[:k], userinfo[k+1:]
			}
			if user != "" || pass != "" {
				return "", "Paste the link without a user name or token in it."
			}
		}
		if j := strings.IndexAny(tail, "?#"); j >= 0 {
			tail = tail[:j]
		}
		h, rest = repoHostOf(netloc), tail
	} else if repoBareRE.MatchString(s) && strings.ToLower(strings.SplitN(s, "/", 2)[0]) != strings.ToLower(CICD_GROUP) {
		parts := strings.SplitN(s, "/", 2)
		h, rest = strings.ToLower(parts[0]), parts[1]
	} else {
		h, rest = host, s
	}
	if h != host {
		return "", "Only projects on " + host + " can be checked (got " + py.Head(h, 80) + ")."
	}
	rest = py.Unquote(rest)
	rest = strings.SplitN(rest, "?", 2)[0]
	rest = strings.SplitN(rest, "#", 2)[0]
	rest = strings.Trim(rest, "/")
	rest = strings.SplitN(rest, "/-/", 2)[0]
	rest = strings.TrimSuffix(rest, ".git")
	rest = strings.Trim(rest, "/")
	if repoIDRE.MatchString(rest) {
		return rest, ""
	}
	if !repoPathRE.MatchString(rest) || strings.Contains(rest, "..") {
		return "", "That is not a GitLab project path (group/project)."
	}
	return rest, ""
}

func repoLatest(target string) Row {
	var r []Row
	if repoIDRE.MatchString(target) {
		n, _ := py.ParseInt(target)
		r = Q("SELECT * FROM cicd_readiness WHERE project_id=? ORDER BY id DESC LIMIT 1", n)
	} else {
		r = Q("SELECT * FROM cicd_readiness WHERE path=? ORDER BY id DESC LIMIT 1", strings.ToLower(target))
	}
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

func repoReportOf(row Row) *py.OMap {
	v, err := py.ParseJSON([]byte(py.OrS(row.Get("report"), "{}")))
	if m := py.AsMap(v); err == nil && m != nil {
		return m
	}
	return py.NewOMap()
}

// RepoReadinessMap is readiness_map(): project id -> [verdict, path] of its newest check.
func RepoReadinessMap() map[int64][2]string {
	out := map[int64][2]string{}
	for _, r := range Q("SELECT r.project_id, r.verdict, r.path FROM cicd_readiness r JOIN (SELECT project_id, max(id) mid FROM " +
		"cicd_readiness WHERE project_id IS NOT NULL GROUP BY project_id) m ON m.mid=r.id") {
		out[r.I("project_id")] = [2]string{py.OrS(r.Get("verdict"), ""), py.OrS(r.Get("path"), "")}
	}
	return out
}

// RepoCountsLine is counts_line(rmap, ids).
func RepoCountsLine(rmap map[int64][2]string, ids []int64) string {
	n := map[string]int{"ready": 0, "warn": 0, "not_ready": 0}
	for _, i := range ids {
		if v, ok := rmap[i]; ok {
			if _, k := n[v[0]]; k {
				n[v[0]]++
			}
		}
	}
	checked := n["ready"] + n["warn"] + n["not_ready"]
	if checked == 0 {
		return ""
	}
	return `<p class="section-note">Readiness (Check a repo, ` + itoa(checked) + ` of ` + itoa(len(ids)) + ` checked): ` +
		`<b>` + itoa(n["ready"]) + `</b> ready · <b>` + itoa(n["warn"]) + `</b> with warnings · <b class="failnum">` +
		itoa(n["not_ready"]) + `</b> not ready. <a href="/cicd/repo">Check a repo</a></p>`
}

func itoa(n int) string { return I2S(int64(n)) }

func repoHex(target string) string {
	const hx = "0123456789abcdef"
	b := make([]byte, 0, len(target)*2)
	for i := 0; i < len(target); i++ {
		b = append(b, hx[target[i]>>4], hx[target[i]&15])
	}
	return string(b)
}

func repoReqName(target string) string { return "repo-check-" + repoHex(target) }

// repoReqState is req_state(target, row): (can start, message, busy).
func repoReqState(target string, row Row) (bool, string, bool) {
	h := repoHex(target)
	if _, err := os.Stat(filepath.Join(SET_IN, "requests", repoReqName(target)+".req")); err == nil {
		return false, "queued", true
	}
	rr := ReadJSONMap(filepath.Join(SET_OUT, "run-requests.json"))
	st := py.AsMap(py.AsMap(rr.Get("repocheck")).Get(h))
	now := Now()
	started := py.Num(py.Or(st.GetD("started", int64(0)), int64(0)))
	done := row != nil && py.Num(py.Or(row.Get("checked_at"), int64(0))) >= started
	if started != 0 && now-started < repoRunningS && !done {
		return false, "running since " + Ago(py.Or(st.GetD("started", int64(0)), int64(0))), true
	}
	if started != 0 && now-started < repoGap {
		return false, "again in 1 min", false
	}
	res := py.OrS(st.GetD("result", ""), "")
	if strings.HasPrefix(res, "skipped") && now-py.Num(py.Or(st.GetD("requested", int64(0)), int64(0))) < 600 {
		return true, strings.ReplaceAll(res, "skipped: ", "Not started: "), false
	}
	return true, "", false
}

func repoLast(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

// RepoPostCheck is post_check(form, sess).
func RepoPostCheck(c *Ctx, form py.QS, sess *Session) (int, string) {
	target, err := RepoParseTarget(form.Get("project"))
	if err != "" {
		return 400, err
	}
	ok, msg, _ := repoReqState(target, repoLatest(target))
	if !ok {
		return 429, "Not started: " + msg + "."
	}
	d := filepath.Join(SET_IN, "requests")
	if e := os.MkdirAll(d, 0o777); e != nil {
		panic(e)
	}
	name := repoReqName(target)
	body := py.Dumps(py.NewOMap("by", sess.Username, "at", NowInt()), -1)
	if e := WriteFileAtomic(filepath.Join(d, name+".req"), filepath.Join(d, "."+name+".tmp"), []byte(body)); e != nil {
		panic(e)
	}
	logf("repo check %s requested by %s", target, sess.Username)
	return 303, "/cicd/repo?" + py.UrlencodeKV("project", target, "sent", "1")
}

// repoDownload is download(rid): (filename, prompt, ok).
func repoDownload(rid string) (string, string, bool) {
	n, ok := fixDigits(rid, 9)
	if !ok {
		return "", "", false
	}
	r := Q("SELECT report FROM cicd_readiness WHERE id=?", n)
	if len(r) == 0 {
		return "", "", false
	}
	h := py.AsMap(repoReportOf(r[0]).Get("handoff"))
	return "argus-fix-repo-" + I2S(n) + ".md", py.OrS(h.Get("prompt"), ""), true
}
