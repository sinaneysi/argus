package board

// gitlab_issues.go: GitLab issues as a second task tracker beside Jira (ADR-0009, ADR-0010), inside the same board
// sync run. Every task names its tracker (tasks.tracker: jira | gitlab); Jira tasks keep their path (sync.go), GitLab
// tasks are pushed and pulled here with one connection in one of two modes (meta "mode"):
//
//	project  the issue board of one project, with that project's access token (a bot user, scope api, Reporter or
//	         Planner). Every issue lives in that project.
//	group    a group issue board, with a group access token. Issues come from every project of the group; each task
//	         keeps its own project (tasks.gl_project) and is shown as "GitLab <path below the group>#<iid>" (gl_ref).
//
// The connection: GitLabReqDir/<user>-<16 hex>.json from Settings -> Integrations (admins only): connect, board,
// project, settings, sync, disconnect, hosts. The settings (no secret) go to EtcDir/gitlab-issues.json; the token
// to EtcDir/gitlab-issues.token (0600; sealed with the master key in the one-volume layout). A connect without a token
// uses the GitLab entry of Settings -> Data sources with purpose "tracker" (GitLabURL, GitLabToken). The status
// (no secret) is Out/gitlab-issues-status.json.
//
// Board mirror: GitLab boards are label lists. Each list maps to a column by its label (the shared status words, a
// scoped label's last part, or the admin's list map); an open issue without a list label is To do, a closed one is
// Done. A move swaps the list label, Done closes, leaving Done reopens. GitLab wins conflicts. Nothing here creates an
// issue by itself: an issue is created only from a task a person made or accepted.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/secret"
)

const (
	glStatusFile     = "gitlab-issues-status.json"
	glMetaFile       = "gitlab-issues.json"
	glTokenFile      = "gitlab-issues.token"
	glHostsFile      = "gitlab-issues-allowed-hosts"
	glMaxFile        = 16 * 1024
	glMaxProjects    = 500
	glReporter       = 20
	glPlanner        = 15
	glClosedImport   = 30 // days: a closed issue the board never saw is imported only if it changed lately
	glAppUID         = 65534
	glGroupTokenHelp = "This is a group board. Use a group access token (the group → Settings → Access tokens, role " +
		"Reporter, scope api); a project access token cannot see the group."
)

// GitLabTokenName is the name the stored token is sealed under (ADR-0021 D4).
const GitLabTokenName = "etc/" + glTokenFile

var (
	glNameRe      = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._@-]{0,63})-([0-9a-f]{16})\.json$`)
	glTokenRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]{20,200}$`)
	glHostRe      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*(:[0-9]{1,5})?$`)
	glProjPathRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}(/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}){1,9}$`)
	glGroupPathRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}(/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}){0,9}$`)
	glUserRe      = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]{0,254}$`)
	glBoardRe     = regexp.MustCompile(`^boards/([0-9]{1,10})(/|$)`)
	glBoardURLRe  = regexp.MustCompile(`/-/boards/([0-9]{1,10})(/|$|\?)`)
	glURLPathRe   = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*$`)
	glISORe       = regexp.MustCompile(`^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d+)?(Z|([+-])(\d\d):?(\d\d))$`)
	glEnvURLRe    = regexp.MustCompile(`^\s*GITLAB_URL\s*=\s*(.*?)\s*$`)
	trackers      = []string{"jira", "gitlab"}
)

// --- the connection ---------------------------------------------------------------------------------------------------

type glList struct {
	Label    string `json:"label"`
	Column   string `json:"column"`
	Position int64  `json:"position"`
}

type glProject struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

type glBoardRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type glBot struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Bot      bool   `json:"bot"`
}

// glMeta is gitlab-issues.json: the same fields the reference job writes, so an imported file reads as it is.
type glMeta struct {
	URL                string            `json:"url,omitempty"`
	Host               string            `json:"host,omitempty"`
	Mode               string            `json:"mode,omitempty"`
	ProjectID          int64             `json:"project_id,omitempty"`
	ProjectPath        string            `json:"project_path,omitempty"`
	ProjectName        string            `json:"project_name,omitempty"`
	WebURL             string            `json:"web_url,omitempty"`
	GroupID            int64             `json:"group_id,omitempty"`
	GroupPath          string            `json:"group_path,omitempty"`
	GroupName          string            `json:"group_name,omitempty"`
	Projects           []glProject       `json:"projects,omitempty"`
	DefaultProjectID   int64             `json:"default_project_id,omitempty"`
	DefaultProjectPath string            `json:"default_project_path,omitempty"`
	BoardID            int64             `json:"board_id,omitempty"`
	BoardName          string            `json:"board_name,omitempty"`
	Boards             []glBoardRef      `json:"boards,omitempty"`
	Lists              []glList          `json:"lists,omitempty"`
	BoardScopeLabels   []map[string]any  `json:"board_scope_labels,omitempty"`
	AccessLevel        *int64            `json:"access_level,omitempty"`
	Bot                *glBot            `json:"bot,omitempty"`
	ConnectedAt        int64             `json:"connected_at,omitempty"`
	By                 string            `json:"by,omitempty"`
	DefaultTracker     string            `json:"default_tracker,omitempty"`
	ListMap            map[string]string `json:"list_map,omitempty"`
	UserMap            map[string]string `json:"user_map,omitempty"`
	TokenFrom          string            `json:"token_from,omitempty"` // "source": the token is the Data sources entry's
}

func (m *glMeta) group() bool { return m.Mode == "group" }

func (o *Options) glPath(name string) string { return filepath.Join(o.EtcDir, name) }

func (o *Options) glMetaRead() *glMeta {
	m := &glMeta{}
	if o.EtcDir == "" {
		return m
	}
	if b, err := os.ReadFile(o.glPath(glMetaFile)); err == nil {
		if json.Unmarshal(b, m) != nil {
			m = &glMeta{}
		}
	}
	return m
}

func (r *run) glSaveMeta(m *glMeta) error {
	if r.o.DryRun {
		return nil
	}
	b, _ := json.MarshalIndent(m, "", " ")
	return glWrite(r.o.glPath(glMetaFile), b, 0o600)
}

func glWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	f.Close()
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// glFileToken is the stored token: a regular file of this user, mode 0600 (opened with the master key when sealed).
func (o *Options) glFileToken() string {
	if o.EtcDir == "" {
		return ""
	}
	p := o.glPath(glTokenFile)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return ""
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(head(string(b), 4096)))
	if secret.Sealed(v) {
		plain, err := o.Key.Open(GitLabTokenName, v)
		if err != nil {
			return ""
		}
		return plain
	}
	return v
}

// glToken is the token of the connection: the stored one, else the Data sources entry's (purpose tracker).
func (o *Options) glToken(m *glMeta) string {
	if m.TokenFrom != "source" {
		if t := o.glFileToken(); t != "" {
			return t
		}
	}
	return o.GitLabToken
}

func (o *Options) glConnected(m *glMeta) bool {
	id := m.ProjectID
	if m.group() {
		id = m.GroupID
	}
	return m.URL != "" && id != 0 && o.glToken(m) != ""
}

func (r *run) glSaveToken(token string) error {
	if r.o.DryRun {
		return nil
	}
	v := token
	if r.o.Key != nil {
		s, err := r.o.Key.Seal(GitLabTokenName, token)
		if err != nil {
			return err
		}
		v = s
	}
	return glWrite(r.o.glPath(glTokenFile), []byte(v), 0o600)
}

// glEnvURL is GITLAB_URL of the configured env file (owned by this user, not group/world-writable), else "".
func (o *Options) glEnvURL() string {
	if o.GitLabEnvFile == "" || !trusted(o.GitLabEnvFile, o.OwnerUID) && !trusted(o.GitLabEnvFile, os.Geteuid()) {
		return ""
	}
	b, err := os.ReadFile(o.GitLabEnvFile)
	if err != nil {
		return ""
	}
	for _, line := range splitLines(head(string(b), 65536)) {
		if mm := glEnvURLRe.FindStringSubmatch(line); mm != nil {
			return strings.TrimRight(strings.Trim(strings.TrimSpace(mm[1]), `'"`), "/")
		}
	}
	return ""
}

// glDefaultURL: the connection's URL, else the Data sources entry's, else the configured GITLAB_URL.
func (o *Options) glDefaultURL(m *glMeta) string {
	for _, u := range []string{m.URL, o.GitLabURL, o.GitLabDefaultURL, o.glEnvURL()} {
		if u != "" {
			return strings.TrimRight(u, "/")
		}
	}
	return ""
}

func glProjectIDs(m *glMeta) map[int64]string {
	out := map[int64]string{}
	if m.group() {
		for _, p := range m.Projects {
			if p.ID != 0 {
				out[p.ID] = p.Path
			}
		}
	} else if m.ProjectID != 0 {
		out[m.ProjectID] = m.ProjectPath
	}
	return out
}

func glDefaultProject(m *glMeta) int64 {
	if m.group() {
		return m.DefaultProjectID
	}
	return m.ProjectID
}

// glCheckProject is the GitLab project a new issue goes to: the person's pick (a project of the connection) or the
// default; 0 when there is none yet.
func glCheckProject(v any, m *glMeta) int64 {
	if orStr(v) == "" {
		return glDefaultProject(m)
	}
	s := strings.TrimSpace(pyStr(v))
	if !isDigits(s) || len(s) > 10 {
		panic(refused("the GitLab project is a project of the connected board"))
	}
	pid, _ := strconv.ParseInt(s, 10, 64)
	if _, ok := glProjectIDs(m)[pid]; !ok {
		panic(refused("that GitLab project is not part of the connected group board"))
	}
	return pid
}

func glShortPath(m *glMeta, path string) string {
	if g := m.GroupPath; g != "" && strings.HasPrefix(path, g+"/") {
		return path[len(g)+1:]
	}
	return path
}

// glRefOf is the issue as the board shows it after "GitLab": "#45" (project board) or "infra#45" (group board).
func glRefOf(m *glMeta, pid, iid int64, full string) string {
	if !m.group() {
		return fmt.Sprintf("#%d", iid)
	}
	path := ""
	if i := strings.LastIndex(full, "#"); i >= 0 {
		path = full[:i]
	}
	if path == "" {
		path = glProjectIDs(m)[pid]
	}
	if path == "" {
		return fmt.Sprintf("#%d", iid)
	}
	return fmt.Sprintf("%s#%d", glShortPath(m, path), iid)
}

// --- the SSRF guard ---------------------------------------------------------------------------------------------------

func (o *Options) glAllowedHosts(m *glMeta) []string {
	if b, err := os.ReadFile(o.glPath(glHostsFile)); err == nil {
		out := []string{}
		for _, x := range splitLines(head(string(b), 65536)) {
			h := strings.ToLower(strings.TrimSpace(x))
			if h != "" && !strings.HasPrefix(h, "#") && glHostRe.MatchString(h) {
				out = append(out, h)
			}
		}
		return out
	}
	out := []string{}
	for _, raw := range []string{o.glEnvURL(), o.GitLabDefaultURL, o.GitLabURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || !(u.Scheme == "https" || (AllowHTTP && u.Scheme == "http")) {
			continue
		}
		h := strings.ToLower(u.Hostname())
		if p := u.Port(); p != "" && p != "443" {
			h += ":" + p
		}
		if !contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// glParseURL is (normalised base URL, host, port) or a refusal. No network.
func glParseURL(raw string) (string, string, string) {
	if len(raw) > 300 || strings.IndexFunc(raw, isSpace) >= 0 {
		panic(refused("the GitLab URL is not valid"))
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil {
		panic(refused("the GitLab URL is not valid"))
	}
	if u.Scheme != "https" && !(AllowHTTP && u.Scheme == "http") {
		panic(refused("the GitLab URL must start with https://"))
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		panic(refused("the GitLab URL must not contain a user name or password"))
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		panic(refused("the GitLab URL must not have a query or fragment"))
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		panic(refused("the GitLab URL has no host"))
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		panic(refused("the GitLab URL has a bad port"))
	}
	if !glURLPathRe.MatchString(u.Path) {
		panic(refused("the GitLab URL path may only hold letters, digits and . _ ~ -"))
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + u.Path, host, port
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}

var metadataIPs = []string{"169.254.169.254", "169.254.170.2", "100.100.100.200", "fd00:ec2::254"}

func badAddress(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if AllowHTTP && ip.IsLoopback() {
		return ""
	}
	for _, m := range metadataIPs {
		if ip.Equal(net.ParseIP(m)) {
			return "a cloud metadata"
		}
	}
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return "link-local"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsMulticast():
		return "multicast"
	case ip.To4() != nil && ip.To4()[0] >= 240:
		return "reserved"
	}
	return ""
}

// glCheckURL is (base, host key, pinned address) for a URL the board may call, else a refusal.
func (r *run) glCheckURL(raw string, hosts []string) (string, string, string) {
	base, host, port := glParseURL(raw)
	key := host
	if port != "443" {
		key = host + ":" + port
	}
	if !contains(hosts, key) {
		have := strings.Join(hosts, ", ")
		if have == "" {
			have = "none"
		}
		panic(refused(fmt.Sprintf("%s is not an allowed GitLab host (Allowed GitLab hosts: %s)", key, have)))
	}
	if net.ParseIP(host) != nil && !AllowHTTP {
		panic(refused("use the GitLab host name, not an address"))
	}
	if r.o.Resolve == nil {
		r.o.Resolve = func(h string) ([]net.IP, error) { return net.DefaultResolver.LookupIP(r.ctx, "ip", h) }
	}
	ips, err := r.o.Resolve(host)
	if err != nil || len(ips) == 0 {
		panic(refused(host + " does not resolve"))
	}
	for _, ip := range ips {
		if why := badAddress(ip); why != "" {
			panic(refused(fmt.Sprintf("%s resolves to %s address %s; refused", host, why, ip)))
		}
	}
	return base, key, ips[0].String()
}

func (r *run) glClient(base, token, pin string) *GitLab {
	mk := r.o.NewGitLab
	if mk == nil {
		mk = func(b, t, p string) (*GitLab, error) { return NewGitLab(b, t, p, "") }
	}
	g, err := mk(base, token, pin)
	if err != nil {
		if ge, ok := asGitLab(err); ok {
			panic(glRefusal{ge})
		}
		panic(refused(err.Error()))
	}
	return g
}

// glRefusal carries a client error out of a request handler.
type glRefusal struct{ err *GitLabError }

func (r *run) glStored(m *glMeta, retries, budget int) *GitLab {
	token := r.o.glToken(m)
	if token == "" || m.URL == "" {
		panic(refused("GitLab issues is not connected"))
	}
	base, _, ip := r.glCheckURL(m.URL, r.o.glAllowedHosts(m))
	g := r.glClient(base, token, ip)
	if retries >= 0 {
		g.Retries, g.Budget, g.MinGap = retries, budget, 0
	}
	return g
}

// --- what the admin typed: project, group, board -----------------------------------------------------------------------

// glParseTarget is (base or "", kind, ref, board id) of what the admin typed; kind is project, group or auto (a path
// that may be either: the project is tried first, then the group).
func glParseTarget(text, base string) (string, string, string, int64) {
	text = strings.TrimRight(strings.TrimSpace(text), "/")
	if text == "" || len(text) > 400 || strings.IndexFunc(text, isSpace) >= 0 {
		panic(refused("enter the issue board's project or group (group/project), or paste its board URL"))
	}
	if isDigits(text) && len(text) <= 10 {
		return "", "project", text, 0
	}
	board := func(rest string) int64 {
		if mm := glBoardRe.FindStringSubmatch(rest); mm != nil {
			n, _ := strconv.ParseInt(mm[1], 10, 64)
			return n
		}
		return 0
	}
	if !strings.Contains(text, "://") {
		path, rest, _ := strings.Cut(text, "/-/")
		b := board(rest)
		if strings.HasPrefix(path, "groups/") {
			if g := path[len("groups/"):]; glGroupPathRe.MatchString(g) {
				return "", "group", g, b
			}
		} else if glProjPathRe.MatchString(path) {
			return "", "auto", path, b
		} else if glGroupPathRe.MatchString(path) {
			return "", "group", path, b
		}
		panic(refused("that is not a project or group path (group/project) or a GitLab URL"))
	}
	u, err := url.Parse(text)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		panic(refused("that is not a project or group path (group/project) or a GitLab URL"))
	}
	urlBase := u.Scheme + "://" + strings.ToLower(u.Host)
	path := u.Path
	if base != "" {
		b, _ := url.Parse(base)
		if b == nil || b.Scheme+"://"+strings.ToLower(b.Host) != urlBase {
			panic(refused(fmt.Sprintf("the pasted URL is on %s, not on the GitLab URL %s", u.Host, base)))
		}
		if b.Path != "" && strings.HasPrefix(path, b.Path+"/") {
			path = path[len(b.Path):]
		}
		urlBase = base
	}
	proj, rest, _ := strings.Cut(strings.Trim(path, "/"), "/-/")
	b := board(rest)
	if strings.HasPrefix(proj, "groups/") {
		g := proj[len("groups/"):]
		if !glGroupPathRe.MatchString(g) {
			panic(refused("could not read a group path from that URL (expected https://host/groups/group[/-/boards/N])"))
		}
		return urlBase, "group", g, b
	}
	if !glProjPathRe.MatchString(proj) {
		panic(refused("could not read a project path from that URL (expected https://host/group/project[/-/boards/N] " +
			"or https://host/groups/group/-/boards/N)"))
	}
	return urlBase, "project", proj, b
}

func glParseBoard(v any) int64 {
	text := strings.TrimSpace(orStr(v))
	if text == "" {
		return 0
	}
	if isDigits(text) && len(text) <= 10 {
		n, _ := strconv.ParseInt(text, 10, 64)
		return n
	}
	if mm := glBoardURLRe.FindStringSubmatch(text); mm != nil {
		n, _ := strconv.ParseInt(mm[1], 10, 64)
		return n
	}
	panic(refused("the board is a number or a pasted board URL (…/-/boards/12)"))
}

// --- lists <-> columns -------------------------------------------------------------------------------------------------

// glListColumn is the board column of a GitLab board list (its label name).
func glListColumn(name string, overrides map[string]string) string {
	n := strings.TrimSpace(name)
	for k, v := range overrides {
		if strings.EqualFold(k, n) && contains(Columns, v) {
			return v
		}
	}
	parts := strings.Split(n, "::")
	words := []string{strings.ToLower(n), strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))}
	for _, w := range words {
		if c, ok := statusDefault[w]; ok {
			return c
		}
	}
	w := words[1]
	for _, ck := range []struct {
		col  string
		keys []string
	}{{"fixer", []string{"fixer"}}, {"done", []string{"done", "closed", "resolved"}},
		{"review", []string{"review", "qa", "test", "verify"}},
		{"progress", []string{"progress", "doing", "develop", "wip", "working"}},
		{"todo", []string{"todo", "to do", "backlog", "open", "ready", "new"}}} {
		for _, k := range ck.keys {
			if strings.Contains(w, k) {
				return ck.col
			}
		}
	}
	return "progress"
}

// glBoardLists are a board's label lists in board order (assignee, milestone and iteration lists are no columns).
func glBoardLists(b map[string]any, overrides map[string]string) []glList {
	out := []glList{}
	ls, _ := b["lists"].([]any)
	for _, x := range ls {
		l, ok := x.(map[string]any)
		if !ok {
			continue
		}
		lab, ok := l["label"].(map[string]any)
		if !ok {
			continue
		}
		name := head(orStr(lab["name"]), 100)
		if name == "" {
			continue
		}
		pos, _ := asInt(l["position"])
		out = append(out, glList{Label: name, Column: glListColumn(name, overrides), Position: pos})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

func glLabels(issue map[string]any) []string {
	var out []string
	l, _ := issue["labels"].([]any)
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// glColumnOf is (column, status name shown) of an issue on the board.
func glColumnOf(issue map[string]any, lists []glList, fixer string) (string, string) {
	labels := glLabels(issue)
	if orStr(issue["state"]) == "closed" {
		return "done", "Closed"
	}
	for _, l := range lists {
		if contains(labels, l.Label) {
			return l.Column, l.Label
		}
	}
	if fixer != "" && contains(labels, fixer) {
		return "fixer", fixer
	}
	return "todo", "Open"
}

// glMoveFields is the update that puts the issue into column to; nil when the board cannot show that column.
func glMoveFields(issue map[string]any, to string, lists []glList, fixer string) object {
	labels := glLabels(issue)
	closed := orStr(issue["state"]) == "closed"
	if to == "done" {
		if closed {
			return object{}
		}
		return object{{"state_event", "close"}}
	}
	target := ""
	var listLabels []string
	for _, l := range lists {
		listLabels = append(listLabels, l.Label)
		if target == "" && l.Column == to {
			target = l.Label
		}
	}
	f := object{}
	if closed {
		f = append(f, kv{"state_event", "reopen"})
	}
	var remove []string
	for _, x := range labels {
		if contains(listLabels, x) && x != target {
			remove = append(remove, x)
		}
	}
	if to != "fixer" && contains(labels, fixer) {
		remove = append(remove, fixer)
	}
	switch {
	case target != "":
		if !contains(labels, target) {
			f = append(f, kv{"add_labels", target})
		}
	case to == "fixer":
		f = append(f, kv{"add_labels", fixer})
	case to != "todo":
		return nil
	}
	if len(remove) > 0 {
		f = append(f, kv{"remove_labels", strings.Join(remove, ",")})
	}
	return f
}

// --- issue text: compose and parse back (Markdown) ----------------------------------------------------------------------

func fence(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "````", "` ` ` `")) }

func (r *run) glCompose(t row) string {
	desc := strings.TrimSpace(t.s("description"))
	if desc == "" {
		desc = "Created from " + r.o.Name + "."
	}
	parts := []string{desc}
	if strings.TrimSpace(t.s("evidence")) != "" {
		parts = append(parts, "### Evidence\n\n````text\n"+fence(t.s("evidence"))+"\n````")
	}
	if strings.TrimSpace(t.s("fix_prompt")) != "" {
		parts = append(parts, "### Fix prompt\n\n````text\n"+fence(t.s("fix_prompt"))+"\n````")
	}
	env := t.s("env")
	if env == "" {
		env = "unknown"
	}
	foot := []string{"env: " + env}
	if t.s("host") != "" {
		foot = append(foot, "host: "+t.s("host"))
	}
	if t.s("source_kind") != "" {
		src := "source: " + t.s("source_kind") + " " + nullStr(t["source_id"])
		if t.s("source_url") != "" {
			src += " (" + r.o.AppURL + t.s("source_url") + ")"
		}
		foot = append(foot, src)
	}
	by := t.s("requested_by")
	if by == "" {
		by = t.s("created_by")
	}
	if by == "" {
		by = "?"
	}
	foot = append(foot, "by: "+by)
	parts = append(parts, "---\n\n"+r.o.Name+" task · "+strings.Join(foot, " · "))
	return strings.Join(parts, "\n\n")
}

func mdBlock(text, name string) (string, bool) {
	re := regexp.MustCompile("(?ms)^### " + name + "[ \\t\\f\\v\\r]*\\n+````text\\n(.*?)\\n?````")
	if m := re.FindStringSubmatch(text); m != nil {
		return strings.ReplaceAll(m[1], "` ` ` `", "````"), true
	}
	return "", false
}

func (r *run) glParse(desc string) parsed {
	desc = strings.ReplaceAll(desc, "\r\n", "\n")
	var p parsed
	if v, ok := mdBlock(desc, "Evidence"); ok {
		p.evidence = &v
	}
	if v, ok := mdBlock(desc, "Fix prompt"); ok {
		p.fixPrompt = &v
	}
	cut := regexp.MustCompile(`(?m)^### (?:Evidence|Fix prompt)[ \t\f\v\r]*$|^---[ \t\f\v\r]*\n+` + regexp.QuoteMeta(r.o.Name) + ` task`)
	h := desc
	if loc := cut.FindStringIndex(desc); loc != nil {
		h = desc[:loc[0]]
	}
	p.description = strings.TrimSpace(h)
	foot := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(r.o.Name) + ` task · (.*)$`)
	if m := foot.FindStringSubmatch(desc); m != nil {
		for _, part := range strings.Split(m[1], " · ") {
			k, v, _ := strings.Cut(part, ": ")
			if k == "env" && contains(envs, v) {
				p.env = v
			} else if k == "host" && hostRe.MatchString(v) {
				p.host = v
			}
		}
	}
	return p
}

// glTS: '2026-10-27T10:00:00.000Z' / '...+03:30' -> unix seconds (0 if unparsable).
func glTS(v any) int64 {
	m := glISORe.FindStringSubmatch(orStr(v))
	if m == nil {
		return 0
	}
	n := func(s string) int { x, _ := strconv.Atoi(s); return x }
	t := time.Date(n(m[1]), time.Month(n(m[2])), n(m[3]), n(m[4]), n(m[5]), n(m[6]), 0, time.UTC).Unix()
	if m[7] == "Z" {
		return t
	}
	off := int64(n(m[9])*3600 + n(m[10])*60)
	if m[8] == "-" {
		off = -off
	}
	return t - off
}

func glISO(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02T15:04:05Z") }

// --- requests from Settings -> Integrations -------------------------------------------------------------------------------

func glAccess(p map[string]any) *int64 {
	perm := asMap(p["permissions"])
	var best *int64
	for _, k := range []string{"project_access", "group_access"} {
		if a, ok := perm[k].(map[string]any); ok {
			n, _ := asInt(a["access_level"])
			if best == nil || n > *best {
				best = &n
			}
		}
	}
	return best
}

func glIssuesOff(p map[string]any) bool {
	return p["issues_enabled"] == false || orStr(p["issues_access_level"]) == "disabled"
}

func idOf(v any) int64 { n, _ := asInt(v); return n }

func (r *run) glMust(v map[string]any, err error) map[string]any {
	if err != nil {
		panic(glRefusal{mustGL(err)})
	}
	return v
}

func mustGL(err error) *GitLabError {
	if g, ok := asGitLab(err); ok {
		return g
	}
	return glerr(0, "", "%s", err.Error())
}

// glResolveBoard is (board id, name, boards, the board) of a project or a group.
func (r *run) glResolveBoard(g *GitLab, id, want int64, group bool) (int64, string, []glBoardRef, map[string]any) {
	var boards []map[string]any
	var err error
	if group {
		boards, err = g.GroupBoards(r.ctx, id)
	} else {
		boards, err = g.Boards(r.ctx, id)
	}
	if err != nil {
		panic(glRefusal{mustGL(err)})
	}
	short := []glBoardRef{}
	for _, b := range boards {
		if bid := idOf(b["id"]); bid != 0 && len(short) < 30 {
			name := orStr(b["name"])
			if name == "" {
				name = "Development"
			}
			short = append(short, glBoardRef{ID: bid, Name: head(name, 80)})
		}
	}
	var bid int64
	switch {
	case want != 0:
		found := false
		var have []string
		for _, b := range short {
			found = found || b.ID == want
			have = append(have, fmt.Sprintf("%s (%d)", b.Name, b.ID))
		}
		if !found {
			h := strings.Join(have, ", ")
			if h == "" {
				h = "none"
			}
			what := "project"
			if group {
				what = "group"
			}
			panic(refused(fmt.Sprintf("board %d is not a board of this %s (boards: %s)", want, what, h)))
		}
		bid = want
	case len(short) == 1:
		bid = short[0].ID
	default:
		return 0, "", short, nil
	}
	var b map[string]any
	for _, x := range boards {
		if idOf(x["id"]) == bid {
			b = x
		}
	}
	if b == nil {
		if group {
			b = r.glMust(g.GroupBoard(r.ctx, id, bid))
		} else {
			b = r.glMust(g.Board(r.ctx, id, bid))
		}
	}
	name := orStr(b["name"])
	if name == "" {
		name = "Development"
	}
	return bid, head(name, 80), short, b
}

func (r *run) glProjects(g *GitLab, gid int64) []glProject {
	ps, err := g.GroupProjects(r.ctx, gid, glMaxProjects)
	if err != nil {
		panic(glRefusal{mustGL(err)})
	}
	out := []glProject{}
	for _, p := range ps {
		if idOf(p["id"]) != 0 && orStr(p["path_with_namespace"]) != "" {
			name := orStr(p["name"])
			if name == "" {
				name = orStr(p["path_with_namespace"])
			}
			out = append(out, glProject{ID: idOf(p["id"]), Path: head(orStr(p["path_with_namespace"]), 200), Name: head(name, 120)})
		}
	}
	return out
}

func (r *run) glPickDefault(g *GitLab, projects []glProject, want any) (int64, string, *int64) {
	var hit *glProject
	if orStr(want) != "" {
		w := strings.TrimSpace(pyStr(want))
		for i := range projects {
			if strconv.FormatInt(projects[i].ID, 10) == w || projects[i].Path == w {
				hit = &projects[i]
				break
			}
		}
		if hit == nil {
			panic(refused(head(w, 80) + " is not a project of this group with issues turned on"))
		}
	} else if len(projects) == 1 {
		hit = &projects[0]
	} else {
		return 0, "", nil
	}
	p := r.glMust(g.Project(r.ctx, strconv.FormatInt(hit.ID, 10)))
	if glIssuesOff(p) {
		panic(refused("issues are turned off in " + hit.Path))
	}
	level := glAccess(p)
	if level != nil && *level < glPlanner {
		panic(refused("the token's role in " + hit.Path + " cannot create issues (Guest); give it Reporter or Planner"))
	}
	return hit.ID, hit.Path, level
}

func (r *run) glAudit(line string) {
	if r.o.DryRun {
		return
	}
	f, err := os.OpenFile(filepath.Join(r.o.Out, "audit.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%s %s\n", r.o.Now().Format("2006-01-02 15:04:05"), line)
	f.Close()
}

func glPairs(text, sep string) [][2]string { return pairs(text, sep) }

// glHandle applies one Settings -> Integrations request (admins only); it returns the result line.
func (r *run) glHandle(req map[string]any, user, role string) string {
	if role != "admin" {
		panic(refused("only admins can change the GitLab issues integration"))
	}
	m := r.o.glMetaRead()
	switch kind := orStr(req["kind"]); kind {
	case "connect":
		token, _ := req["token"].(string)
		fromSource := false
		if token == "" && r.o.GitLabToken != "" {
			token, fromSource = r.o.GitLabToken, true
		} else if !glTokenRe.MatchString(token) {
			panic(refused("that does not look like a GitLab access token (project: Settings → Access tokens)"))
		}
		raw := strings.TrimSpace(orStr(req["url"]))
		urlBase, kindT, ref, board := glParseTarget(orStr(req["project"]), raw)
		if raw == "" {
			raw = urlBase
		}
		if raw == "" && fromSource {
			raw = r.o.GitLabURL
		}
		if raw == "" {
			raw = r.o.glDefaultURL(m)
		}
		if raw == "" {
			panic(refused("enter the GitLab URL, or paste the board's full URL"))
		}
		want := board
		if orStr(req["board"]) != "" {
			want = glParseBoard(req["board"])
		}
		base, key, ip := r.glCheckURL(raw, r.o.glAllowedHosts(m))
		g := r.glClient(base, token, ip)
		g.Retries, g.Budget, g.MinGap = 1, 40, 0
		me, err := g.User(r.ctx)
		if err != nil {
			panic(glRefusal{mustGL(err)})
		}
		n := &glMeta{DefaultTracker: m.DefaultTracker, ListMap: m.ListMap, UserMap: m.UserMap}
		var b map[string]any
		var p map[string]any
		if kindT == "project" || kindT == "auto" {
			p, err = g.Project(r.ctx, ref)
			if err != nil {
				ge := mustGL(err)
				if ge.Status != 404 {
					panic(glRefusal{ge})
				}
				p = nil
				if kindT == "project" {
					panic(refused("project " + ref + " not found, or this token cannot see it (a project access token sees " +
						"only its own project; for a group board paste the group's board URL, " +
						".../groups/<group>/-/boards, and use a group access token)"))
				}
			}
		}
		if p != nil {
			if glIssuesOff(p) {
				path := orStr(p["path_with_namespace"])
				if path == "" {
					path = ref
				}
				panic(refused("issues are turned off in " + path + " (Settings → General → Visibility)"))
			}
			level := glAccess(p)
			if level != nil && *level < glPlanner {
				panic(refused("the token's role cannot change issues (Guest); give it Reporter or Planner"))
			}
			n.Mode, n.ProjectID = "project", idOf(p["id"])
			n.ProjectPath = head(orDefaultS(orStr(p["path_with_namespace"]), ref), 200)
			n.ProjectName = head(orDefaultS(orStr(p["name_with_namespace"]), orDefaultS(orStr(p["name"]), ref)), 200)
			n.WebURL, n.AccessLevel = head(orStr(p["web_url"]), 300), level
			n.BoardID, n.BoardName, n.Boards, b = r.glResolveBoard(g, n.ProjectID, want, false)
		} else {
			gr, err := g.Group(r.ctx, ref)
			if err != nil {
				if ge := mustGL(err); ge.Status == 403 || ge.Status == 404 {
					panic(refused("group " + ref + " not found, or this token cannot see it. " + glGroupTokenHelp))
				}
				panic(glRefusal{mustGL(err)})
			}
			gid := idOf(gr["id"])
			projects := r.glProjects(g, gid)
			if len(projects) == 0 {
				panic(refused("the token sees no project with issues in " + orDefaultS(orStr(gr["full_path"]), ref) +
					"; check that it is a group access token of this group with role Reporter"))
			}
			n.Mode, n.GroupID, n.Projects = "group", gid, projects
			n.GroupPath = head(orDefaultS(orStr(gr["full_path"]), ref), 200)
			n.GroupName = head(orDefaultS(orStr(gr["full_name"]), orDefaultS(orStr(gr["name"]), ref)), 200)
			n.WebURL = head(orStr(gr["web_url"]), 300)
			n.BoardID, n.BoardName, n.Boards, b = r.glResolveBoard(g, gid, want, true)
			n.DefaultProjectID, n.DefaultProjectPath, n.AccessLevel = r.glPickDefault(g, projects, req["default_project"])
		}
		if fromSource {
			n.TokenFrom = "source"
		} else if err := r.glSaveToken(token); err != nil {
			panic(refused("could not save (" + err.Error() + ")"))
		}
		n.URL, n.Host = base, key
		n.Lists = []glList{}
		if n.BoardID != 0 {
			n.Lists = glBoardLists(b, n.ListMap)
		}
		n.Bot = &glBot{Username: head(orStr(me["username"]), 80), Name: head(orStr(me["name"]), 80), Bot: me["bot"] == true}
		n.ConnectedAt, n.By = r.now(), user
		if err := r.glSaveMeta(n); err != nil {
			panic(refused("could not save (" + err.Error() + ")"))
		}
		where := "project " + n.ProjectPath
		if n.group() {
			where = "group " + n.GroupPath
		}
		bid := "-"
		if n.BoardID != 0 {
			bid = strconv.FormatInt(n.BoardID, 10)
		}
		r.glAudit(fmt.Sprintf("gitlab-issues: connected to %s %s board %s as %s by %s", key, where, bid, n.Bot.Username, user))
		tail := "no board yet: Open and Closed only"
		switch {
		case n.BoardID != 0:
			tail = "board " + n.BoardName
		case len(n.Boards) > 0:
			tail = fmt.Sprintf("%d boards: pick one", len(n.Boards))
		}
		if n.group() {
			tail += fmt.Sprintf("; %d projects", len(n.Projects))
			if n.DefaultProjectID == 0 {
				tail += ", choose the default project"
			}
		}
		to := n.ProjectPath
		if n.group() {
			to = where
		}
		via := ""
		if fromSource {
			via = " (token of the GitLab data source)"
		}
		return "connected as " + orDefaultS(n.Bot.Username, "?") + " to " + to + via + "; " + tail
	case "board":
		if !r.o.glConnected(m) {
			panic(refused("connect GitLab issues first"))
		}
		want := glParseBoard(req["board"])
		if want == 0 {
			panic(refused("choose a board"))
		}
		g := r.glStored(m, 1, 10)
		id := m.ProjectID
		if m.group() {
			id = m.GroupID
		}
		bid, bname, boards, b := r.glResolveBoard(g, id, want, m.group())
		m.BoardID, m.BoardName, m.Boards, m.Lists = bid, bname, boards, glBoardLists(b, m.ListMap)
		if err := r.glSaveMeta(m); err != nil {
			panic(refused("could not save (" + err.Error() + ")"))
		}
		r.glAudit(fmt.Sprintf("gitlab-issues: board %d (%s) chosen by %s", bid, bname, user))
		return fmt.Sprintf("board %s chosen (%d lists)", bname, len(m.Lists))
	case "project":
		if !r.o.glConnected(m) || !m.group() {
			panic(refused("the default project is for a connected group board"))
		}
		g := r.glStored(m, 1, 20)
		projects := r.glProjects(g, m.GroupID)
		if len(projects) == 0 {
			projects = m.Projects
		}
		pid, path, level := r.glPickDefault(g, projects, req["project"])
		if pid == 0 {
			panic(refused("choose a project"))
		}
		m.Projects, m.DefaultProjectID, m.DefaultProjectPath, m.AccessLevel = projects, pid, path, level
		if err := r.glSaveMeta(m); err != nil {
			panic(refused("could not save (" + err.Error() + ")"))
		}
		r.glAudit("gitlab-issues: default project " + path + " chosen by " + user)
		return "new GitLab issues go to " + path + " unless the person picks another project"
	case "settings":
		tr := orDefaultS(orStr(req["default_tracker"]), "jira")
		if !contains(trackers, tr) {
			panic(refused("the default tracker is Jira or GitLab"))
		}
		if tr == "gitlab" && !r.o.glConnected(m) {
			panic(refused("connect GitLab issues before making it the default"))
		}
		lm, um := map[string]string{}, map[string]string{}
		for _, p := range glPairs(orStr(req["list_map"]), ";") {
			if !contains(Columns, p[1]) || runes(p[0]) > 100 {
				panic(refused(fmt.Sprintf("list map: %s=%s: the column is one of %s", head(p[0], 40), head(p[1], 20), strings.Join(Columns, ", "))))
			}
			lm[p[0]] = p[1]
		}
		for _, p := range glPairs(orStr(req["user_map"]), ",") {
			if !userRe.MatchString(p[0]) || !glUserRe.MatchString(p[1]) {
				panic(refused(fmt.Sprintf("user map: %s=%s is not %s.user=gitlab.user", head(p[0], 40), head(p[1], 40), r.o.Label)))
			}
			um[p[0]] = p[1]
		}
		m.DefaultTracker, m.ListMap, m.UserMap = tr, lm, um
		for i := range m.Lists {
			m.Lists[i].Column = glListColumn(m.Lists[i].Label, lm)
		}
		if err := r.glSaveMeta(m); err != nil {
			panic(refused("could not save (" + err.Error() + ")"))
		}
		r.glAudit(fmt.Sprintf("gitlab-issues: settings by %s: default tracker %s, %d list mappings, %d user mappings", user, tr, len(lm), len(um)))
		return "saved: new tasks go to " + map[bool]string{true: "GitLab", false: "Jira"}[tr == "gitlab"] + " by default"
	case "sync":
		r.setState("gl_want_full", "1")
		if r.o.glConnected(m) {
			return "sync requested; it runs within seconds"
		}
		return "GitLab issues is not connected"
	case "disconnect":
		if !r.o.DryRun {
			os.Remove(r.o.glPath(glTokenFile))
		}
		if err := r.glSaveMeta(&glMeta{ListMap: m.ListMap, UserMap: m.UserMap, DefaultTracker: "jira"}); err != nil {
			panic(refused("could not save (" + err.Error() + ")"))
		}
		r.glAudit("gitlab-issues: disconnected by " + user)
		return "disconnected; the token was deleted (revoke it in GitLab too). GitLab tasks stay on the board, read-only"
	case "hosts":
		hs, ok := req["hosts"].([]any)
		if !ok || len(hs) > 10 {
			panic(refused("up to 10 hosts"))
		}
		var clean []string
		for _, h := range hs {
			s := strings.ToLower(strings.TrimSpace(pyStr(h)))
			if !glHostRe.MatchString(s) {
				panic(refused("not a host name: " + head(s, 60)))
			}
			if !contains(clean, s) {
				clean = append(clean, s)
			}
		}
		if !r.o.DryRun {
			text := ""
			for _, h := range clean {
				text += h + "\n"
			}
			if err := glWrite(r.o.glPath(glHostsFile), []byte(text), 0o644); err != nil {
				panic(refused("could not save (" + err.Error() + ")"))
			}
		}
		list := strings.Join(clean, ", ")
		if list == "" {
			list = "(none)"
		}
		r.glAudit("gitlab-issues: allowed hosts set by " + user + ": " + list)
		return "saved"
	}
	panic(refused("unknown request kind"))
}

func orDefaultS(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// glSafeHandle turns a refusal or a client error into a result line.
func (r *run) glSafeHandle(req map[string]any, user, role string) (res string) {
	defer func() {
		if x := recover(); x != nil {
			switch e := x.(type) {
			case refused:
				res = "refused: " + string(e)
			case glRefusal:
				if e.err.Kind == "auth" {
					res = "refused: " + e.err.Msg + ". Check that the whole token was pasted, that it has the api scope and has not expired."
				} else {
					res = "refused: " + e.err.Msg
				}
			default:
				panic(x)
			}
		}
	}()
	return r.glHandle(req, user, role)
}

func glReadRequest(path string) (map[string]any, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot open request (%s)", strerror(err))
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("request is not a regular file")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || (int(st.Uid) != glAppUID && int(st.Uid) != os.Geteuid()) {
		return nil, errors.New("request not written by the app")
	}
	data, _ := io.ReadAll(io.LimitReader(f, glMaxFile+1))
	if len(data) > glMaxFile {
		return nil, errors.New("request file too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil || !json.Valid(data) {
		return nil, errors.New("request is not valid JSON")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("request is not a JSON object")
	}
	return m, nil
}

// glProcessRequests applies GitLabReqDir/<user>-<16 hex>.json; results go to r.glResults.
func (r *run) glProcessRequests() {
	if r.o.GitLabReqDir == "" {
		return
	}
	ents, err := os.ReadDir(r.o.GitLabReqDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(r.o.GitLabReqDir, name)
		mm := glNameRe.FindStringSubmatch(name)
		if mm == nil {
			if !strings.HasPrefix(name, ".") && !r.o.DryRun {
				os.Remove(path)
			}
			continue
		}
		user, rid := mm[1], mm[2]
		req, rerr := glReadRequest(path)
		if req == nil {
			req = map[string]any{}
		}
		token, _ := req["token"].(string)
		var result string
		switch {
		case rerr != nil:
			result = "refused: " + rerr.Error()
		case orStr(req["user"]) != user:
			result = "refused: request user does not match its file name"
		default:
			if role := r.userRole(user); role == "" {
				result = "refused: unknown or disabled user"
			} else {
				result = r.glSafeHandle(req, user, role)
			}
		}
		if !r.o.DryRun {
			os.Remove(path)
		}
		if token != "" {
			result = strings.ReplaceAll(result, token, "[REDACTED]")
		}
		r.glResults[rid] = object{{"user", user}, {"kind", head(orStr(req["kind"]), 20)}, {"result", head(result, 400)}, {"at", r.now()}}
		r.log().Info("gitlab-issues request", "id", rid, "kind", orDefaultS(orStr(req["kind"]), "?"), "by", user, "result", head(result, 200))
	}
}

// --- status for the app ------------------------------------------------------------------------------------------------

func nz(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func ns(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (r *run) glPublish(run object) error {
	path := filepath.Join(r.o.Out, glStatusFile)
	var old map[string]any
	if b, err := os.ReadFile(path); err == nil {
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		d.Decode(&old)
	}
	res := map[string]any{}
	for k, v := range asMap(old["results"]) {
		res[k] = v
	}
	for k, v := range r.glResults {
		res[k] = v
	}
	keys := make([]string, 0, len(res))
	for k := range res {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 60 {
		keys = keys[len(keys)-60:]
	}
	ro := object{}
	for _, k := range keys {
		ro = append(ro, kv{k, res[k]})
	}
	m := r.o.glMetaRead()
	conn := object{}
	connected := r.o.glConnected(m)
	if connected {
		var bot any
		if m.Bot != nil {
			bot = object{{"username", m.Bot.Username}, {"name", m.Bot.Name}, {"bot", m.Bot.Bot}}
		}
		var lvl any
		if m.AccessLevel != nil {
			lvl = *m.AccessLevel
		}
		mode := "project"
		if m.group() {
			mode = "group"
		}
		conn = object{{"url", ns(m.URL)}, {"host", ns(m.Host)}, {"project_id", nz(m.ProjectID)}, {"project_path", ns(m.ProjectPath)},
			{"project_name", ns(m.ProjectName)}, {"web_url", ns(m.WebURL)}, {"board_id", nz(m.BoardID)}, {"board_name", ns(m.BoardName)},
			{"bot", bot}, {"access_level", lvl}, {"connected_at", nz(m.ConnectedAt)}, {"by", ns(m.By)}, {"group_id", nz(m.GroupID)},
			{"group_path", ns(m.GroupPath)}, {"group_name", ns(m.GroupName)}, {"default_project_id", nz(m.DefaultProjectID)},
			{"default_project_path", ns(m.DefaultProjectPath)}, {"mode", mode}}
		if m.TokenFrom == "source" {
			conn = append(conn, kv{"token_from", "Settings → Data sources"})
		}
	}
	warn := []any{}
	if connected {
		what := "project"
		if m.group() {
			what = "group"
		}
		if m.Bot == nil || !m.Bot.Bot {
			warn = append(warn, "This is a personal token. Replace it with a "+what+" access token, so the integration does not depend on one person's account.")
		}
		if m.group() && m.DefaultProjectID == 0 {
			warn = append(warn, "Choose the default project: a new GitLab issue needs a project, and a person may not pick one.")
		}
		if m.AccessLevel != nil && *m.AccessLevel > 30 {
			warn = append(warn, "The token's role is higher than it needs (Reporter or Planner is enough for issues).")
		}
		if m.BoardID == 0 {
			if len(m.Boards) == 0 {
				warn = append(warn, "No issue board chosen: only Open and Closed are mirrored.")
			} else {
				warn = append(warn, "Choose the issue board; until then only Open and Closed are mirrored.")
			}
		}
	}
	boards, lists, projects := []any{}, []any{}, []any{}
	if connected {
		for _, b := range m.Boards {
			boards = append(boards, object{{"id", b.ID}, {"name", b.Name}})
		}
		for _, l := range m.Lists {
			lists = append(lists, object{{"label", l.Label}, {"column", l.Column}, {"position", l.Position}})
		}
		if m.group() {
			for _, p := range m.Projects {
				projects = append(projects, object{{"id", p.ID}, {"path", p.Path}, {"short", glShortPath(m, p.Path)}})
			}
		}
	}
	lmap, umap := object{}, object{}
	for _, k := range sortedStrKeys(m.ListMap) {
		lmap = append(lmap, kv{k, m.ListMap[k]})
	}
	for _, k := range sortedStrKeys(m.UserMap) {
		umap = append(umap, kv{k, m.UserMap[k]})
	}
	var last any = run
	if run == nil {
		last = old["last_run"]
		if last == nil {
			last = object{}
		}
	}
	hosts := []any{}
	for _, h := range r.o.glAllowedHosts(m) {
		hosts = append(hosts, h)
	}
	doc := object{{"updated_at", r.now()}, {"connected", connected}, {"connection", conn}, {"boards", boards}, {"lists", lists},
		{"projects", projects}, {"settings", object{{"default_tracker", orDefaultS(m.DefaultTracker, "jira")}, {"list_map", lmap},
			{"user_map", umap}}}, {"default_url", r.o.glDefaultURL(m)}, {"allowed_hosts", hosts}, {"warnings", warn},
		{"last_run", last}, {"results", ro}}
	if r.o.DryRun {
		return nil
	}
	if err := os.MkdirAll(r.o.Out, 0o755); err != nil {
		return err
	}
	return writeAtomic(path, pyDumpsIndent(doc, 1), 0o644)
}

func sortedStrKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- the board requests of GitLab tasks (create, move, comment, assign) ------------------------------------------------

func (r *run) glUser(m *glMeta, user string) string {
	if v, ok := m.UserMap[user]; ok {
		return v
	}
	return user
}

// --- push: the queued changes of GitLab tasks ---------------------------------------------------------------------------

func (r *run) glUserID(g *GitLab, cache map[string]int64, name string) (int64, error) {
	if name == "" || !glUserRe.MatchString(name) {
		return 0, nil
	}
	if id, ok := cache[name]; ok {
		return id, nil
	}
	u, err := g.UserByName(r.ctx, name)
	if err != nil {
		return 0, err
	}
	cache[name] = idOf(u["id"])
	return cache[name], nil
}

func (r *run) glPush(g *GitLab, m *glMeta) error {
	ids, blocked := map[string]int64{}, map[int64]bool{}
	for _, op := range r.rows(`SELECT o.* FROM outbox o JOIN tasks t ON t.id=o.task_id WHERE o.state='queued' AND o.next_at<=? `+
		`AND t.tracker='gitlab' ORDER BY o.id`, r.now()) {
		tid := op.i("task_id")
		t := r.one(`SELECT * FROM tasks WHERE id=?`, tid)
		if blocked[tid] || t == nil {
			continue
		}
		var p map[string]any
		d := json.NewDecoder(strings.NewReader(orObj(op.s("payload"))))
		d.UseNumber()
		d.Decode(&p)
		if p == nil {
			p = map[string]any{}
		}
		kind := op.s("kind")
		if kind != "create" && t.null("gl_iid") {
			blocked[tid] = true
			continue
		}
		pid := m.ProjectID
		if m.group() {
			pid = t.i("gl_project")
			if pid == 0 {
				pid = glDefaultProject(m)
			}
		}
		if !t.null("gl_iid") && t.i("gl_project") != 0 && t.i("gl_project") != pid {
			r.must(`UPDATE outbox SET state='dropped', last_error=?, done_at=? WHERE id=?`,
				"the issue lives in another GitLab project than the connected one", r.now(), op.i("id"))
			r.commit()
			continue
		}
		err := r.glPushOne(g, m, op, t, p, pid, ids)
		if err == errDone {
			continue
		}
		if err == nil {
			r.stats["pushed"]++
			r.must(`UPDATE outbox SET state='done', done_at=?, last_error=NULL WHERE id=?`, r.now(), op.i("id"))
			r.commit()
			continue
		}
		ge := mustGL(err)
		if glStop(ge) {
			r.must(`UPDATE outbox SET last_error=? WHERE id=?`, "gitlab: "+head(ge.Msg, 250), op.i("id"))
			r.commit()
			return ge
		}
		attempts := op.i("attempts") + 1
		switch {
		case (ge.Status == 0 || ge.Status >= 500) && attempts < 10:
			r.must(`UPDATE outbox SET attempts=?, next_at=?, last_error=? WHERE id=?`, attempts, r.now()+min(int64(60)<<attempts, 3600), head(ge.Msg, 300), op.i("id"))
		case p["optional"] == true:
			r.must(`UPDATE outbox SET state='dropped', attempts=?, last_error=?, done_at=? WHERE id=?`, attempts, head(ge.Msg, 300), r.now(), op.i("id"))
			r.event(tid, "gitlab", r.o.Label, "Not written to GitLab ("+kind+"): "+head(ge.Msg, 200))
		default:
			r.must(`UPDATE outbox SET state='failed', attempts=?, last_error=?, done_at=? WHERE id=?`, attempts, head(ge.Msg, 300), r.now(), op.i("id"))
			r.event(tid, "error", r.o.Label, "GitLab "+kind+" failed: "+head(ge.Msg, 200))
			r.stats["failed"]++
		}
		blocked[tid] = true
		r.commit()
	}
	return nil
}

func listJSON(labels []string) string {
	if len(labels) > 30 {
		labels = labels[:30]
	}
	return string(pyDumps(listAny(labels)))
}

func (r *run) glPushOne(g *GitLab, m *glMeta, op, t row, p map[string]any, pid int64, ids map[string]int64) error {
	ctx, tid, iid := r.ctx, t.i("id"), t.i("gl_iid")
	fixer := r.fixerLabel()
	switch op.s("kind") {
	case "create":
		if pid == 0 {
			return glerr(400, "", "no GitLab project for this issue: choose the default project (Settings → Integrations → "+
				"GitLab issues) or pick one on the task")
		}
		if _, ok := glProjectIDs(m)[pid]; !ok {
			return glerr(400, "", "the chosen GitLab project is not part of the connected board")
		}
		labels := jsonList(orJSON(t.s("labels")))
		for _, l := range m.Lists {
			if l.Column == "todo" {
				labels = append(labels, l.Label)
				break
			}
		}
		f := object{{"title", head(r.scan(t.s("title"), fmt.Sprintf("task:%d:summary", tid)), 255)},
			{"description", r.scan(r.glCompose(t), fmt.Sprintf("task:%d:description", tid))},
			{"labels", strings.Join(dedupe(labels), ",")}}
		if a := orStr(p["assignee"]); a != "" {
			uid, err := r.glUserID(g, ids, r.glUser(m, a))
			if err != nil {
				return err
			}
			switch {
			case uid != 0:
				f = append(f, kv{"assignee_ids", []any{uid}})
			case p["assign_soft"] == true:
				r.event(tid, "gitlab", r.o.Label, "Not assigned to "+a+": no such GitLab user")
			default:
				return glerr(400, "", "no GitLab user %s", a)
			}
		}
		res, err := g.CreateIssue(ctx, pid, f)
		if err != nil {
			return err
		}
		niid := idOf(res["iid"])
		if niid == 0 {
			return glerr(500, "", "GitLab returned no issue number")
		}
		_, name := glColumnOf(res, m.Lists, fixer)
		gref := glRefOf(m, pid, niid, orStr(asMap(res["references"])["full"]))
		r.must(`UPDATE tasks SET gl_project=?, gl_iid=?, gl_url=?, gl_ref=?, jira_status=?, synced_at=? WHERE id=?`,
			pid, niid, head(orStr(res["web_url"]), 300), gref, name, r.now(), tid)
		r.must(`INSERT OR IGNORE INTO task_seen VALUES (?,?,?)`, tid, "gitlab", r.now())
		r.event(tid, "gitlab", "gitlab", "Created in GitLab as "+gref)
	case "transition":
		to := orStr(p["to"])
		cur, err := g.Issue(ctx, pid, iid)
		if err != nil {
			return err
		}
		col, name := glColumnOf(cur, m.Lists, fixer)
		upd := glTS(cur["updated_at"])
		at, _ := asInt(p["at"])
		if orStr(p["from_jira"]) != "" && name != pyStr(p["from_jira"]) && upd > at && col != to {
			r.must(`UPDATE outbox SET state='dropped', last_error=?, done_at=? WHERE id=?`, "conflict: GitLab moved it to "+name+" meanwhile", r.now(), op.i("id"))
			r.must(`UPDATE tasks SET status=?, jira_status=?, updated_at=? WHERE id=?`, col, name, r.now(), tid)
			r.event(tid, "conflict", r.o.Label, "Move to "+orDefaultS(columnLabel[to], to)+" dropped: GitLab changed this issue meanwhile ("+name+"); GitLab wins")
			r.stats["conflicts"]++
			r.commit()
			return errDone
		}
		if col != to || (to != "fixer" && contains(glLabels(cur), fixer)) {
			f := glMoveFields(cur, to, m.Lists, fixer)
			if f == nil {
				return glerr(409, "", "the GitLab board has no list for %s (add one, or map a list in Settings → Integrations → GitLab issues)", orDefaultS(columnLabel[to], to))
			}
			if len(f) > 0 {
				if cur, err = g.UpdateIssue(ctx, pid, iid, f); err != nil {
					return err
				}
				_, name = glColumnOf(cur, m.Lists, fixer)
			}
			if to == "fixer" && orStr(f.get("add_labels")) == fixer {
				r.setLabel(tid, fixer, true)
			}
		}
		r.must(`UPDATE tasks SET jira_status=?, labels=?, synced_at=? WHERE id=?`, name, listJSON(glLabels(cur)), r.now(), tid)
	case "label":
		lb, k, add := orStr(p["remove"]), "remove_labels", false
		if lb == "" {
			lb, k, add = pyStr(p["add"]), "add_labels", true
		}
		if _, err := g.UpdateIssue(ctx, pid, iid, object{{k, lb}}); err != nil {
			return err
		}
		r.setLabel(tid, lb, add)
	case "comment":
		body := str(p["body"])
		local, _ := asInt(p["local"])
		if local != 0 {
			body = "**[" + r.o.Name + " · " + orDefaultS(orStr(p["as"]), "?") + "]** " + body
		}
		res, err := g.AddNote(ctx, pid, iid, head(r.scan(body, fmt.Sprintf("task:%d:comment", tid)), maxComment+200))
		if err != nil {
			return err
		}
		if local != 0 {
			var jid any
			if orStr(res["id"]) != "" {
				jid = fmt.Sprintf("gl:%d:%s", pid, pyStr(res["id"]))
			}
			r.must(`UPDATE comments SET jira_id=?, pending=0 WHERE id=?`, jid, local)
		}
	case "assign":
		name := r.glUser(m, orStr(p["user"]))
		uid, err := r.glUserID(g, ids, name)
		if err != nil {
			return err
		}
		if uid == 0 {
			return glerr(400, "", "no GitLab user %s (map %s users to GitLab users in Settings → Integrations → GitLab issues)", name, r.o.Name)
		}
		if _, err := g.UpdateIssue(ctx, pid, iid, object{{"assignee_ids", []any{uid}}}); err != nil {
			return err
		}
		r.must(`UPDATE tasks SET assignee=? WHERE id=?`, name, tid)
	}
	return nil
}

// --- pull --------------------------------------------------------------------------------------------------------------

func glPriority(labels []string) any {
	for _, lb := range labels {
		parts := strings.Split(lb, "::")
		w := strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))
		low := strings.ToLower(lb)
		if strings.HasPrefix(low, "priority") || strings.HasPrefix(low, "prio") {
			for _, p := range priorities {
				if strings.ToLower(p) == w {
					return p
				}
			}
		}
	}
	return nil
}

func (r *run) glUpsert(g *GitLab, m *glMeta, issue map[string]any, withNotes bool) error {
	pid := m.ProjectID
	if m.group() {
		pid = idOf(issue["project_id"])
	}
	iid := idOf(issue["iid"])
	if iid == 0 || pid == 0 {
		return nil
	}
	gref := glRefOf(m, pid, iid, orStr(asMap(issue["references"])["full"]))
	labels := glLabels(issue)
	if len(labels) > 30 {
		labels = labels[:30]
	}
	col, name := glColumnOf(issue, m.Lists, "") // the fixer column is retired: its label is an ordinary label
	ps := r.glParse(orStr(issue["description"]))
	env := ps.env
	if env == "" {
		for _, lb := range labels {
			if strings.HasPrefix(lb, "env-") && contains(envs, lb[4:]) {
				env = lb[4:]
				break
			}
		}
	}
	asg := map[string]any{}
	if l, ok := issue["assignees"].([]any); ok && len(l) > 0 {
		asg = asMap(l[0])
	} else {
		asg = asMap(issue["assignee"])
	}
	upd := glTS(issue["updated_at"])
	if upd == 0 {
		upd = r.now()
	}
	title := head(orStr(issue["title"]), maxTitle)
	if title == "" {
		title = head(fmt.Sprintf("#%d", iid), maxTitle)
	}
	weburl := head(orStr(issue["web_url"]), 300)
	t := r.one(`SELECT * FROM tasks WHERE gl_project=? AND gl_iid=?`, pid, iid)
	var tid int64
	if t == nil {
		if orStr(issue["state"]) == "closed" && upd < r.now()-glClosedImport*86400 {
			return nil
		}
		kind, sid := r.labelsSource(labels)
		if kind != nil && r.one(`SELECT 1 FROM tasks WHERE source_kind=? AND source_id=?`, kind, sid) != nil {
			kind, sid = nil, nil
		}
		by := "human"
		if contains(labels, r.o.Label+"-auto") {
			by = "auto"
		} else if contains(labels, r.o.Label+"-proposed") {
			by = "proposal"
		}
		created := glTS(issue["created_at"])
		if created == 0 {
			created = upd
		}
		res := r.must(`INSERT INTO tasks (tracker, gl_project, gl_iid, gl_url, gl_ref, title, description, evidence, fix_prompt, status, `+
			`jira_status, assignee, assignee_name, priority, labels, env, host, source_kind, source_id, created_by, created_at, `+
			`updated_at, jira_updated, synced_at) VALUES ('gitlab',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			pid, iid, weburl, gref, title, ps.description, ptr(ps.evidence), ptr(ps.fixPrompt), col, name, strOrNil(asg["username"]),
			strOrNil(asg["name"]), glPriority(labels), listJSON(labels), orNil(env), orNil(ps.host), kind, sid, by, created, upd, upd, r.now())
		tid, _ = res.LastInsertId()
		r.event(tid, "gitlab", "gitlab", "Seen in GitLab ("+name+")")
		r.stats["new"]++
		withNotes = true
	} else {
		tid = t.i("id")
		prev := t.s("status")
		pending := r.one(`SELECT 1 FROM outbox WHERE task_id=? AND kind IN ('transition','label') AND state='queued' AND created_at>=?`, tid, upd) != nil
		if pending {
			col = prev
		}
		if weburl == "" {
			weburl = t.s("gl_url")
		}
		sets := object{{"title", title}, {"description", ps.description}, {"status", col}, {"jira_status", name},
			{"assignee", strOrNil(asg["username"])}, {"assignee_name", strOrNil(asg["name"])}, {"labels", listJSON(labels)},
			{"jira_updated", upd}, {"synced_at", r.now()}, {"gone", int64(0)}, {"gl_url", orNil(weburl)}, {"gl_ref", gref}}
		if ps.evidence != nil {
			sets = append(sets, kv{"evidence", *ps.evidence})
		}
		if ps.fixPrompt != nil {
			sets = append(sets, kv{"fix_prompt", *ps.fixPrompt})
		}
		if env != "" {
			sets = append(sets, kv{"env", env})
		}
		if ps.host != "" {
			sets = append(sets, kv{"host", ps.host})
		}
		var changed []string
		for _, k := range []string{"title", "status", "assignee", "fix_prompt"} {
			for _, s := range sets {
				if s.k == k && !sameStr(s.v, t[k]) {
					changed = append(changed, k)
				}
			}
		}
		if len(changed) > 0 {
			sets = append(sets, kv{"updated_at", r.now()})
			r.stats["updated"]++
		}
		var cols []string
		var args []any
		for _, s := range sets {
			cols = append(cols, s.k+"=?")
			args = append(args, s.v)
		}
		r.must("UPDATE tasks SET "+strings.Join(cols, ", ")+" WHERE id=?", append(args, tid)...)
		if contains(changed, "status") && !pending {
			r.event(tid, "gitlab", "gitlab", "GitLab: "+orDefaultS(columnLabel[prev], prev)+" → "+columnLabel[col]+" ("+name+")")
		}
		if contains(changed, "assignee") {
			who := orDefaultS(orStr(asg["name"]), orDefaultS(orStr(asg["username"]), "nobody"))
			r.event(tid, "gitlab", "gitlab", "GitLab: assigned to "+who)
		}
	}
	r.must(`INSERT OR REPLACE INTO task_seen VALUES (?,?,?)`, tid, "gitlab", r.now())
	if n, _ := asInt(issue["user_notes_count"]); withNotes && n > 0 {
		notes, err := g.Notes(r.ctx, pid, iid)
		if err != nil {
			return err
		}
		if len(notes) > 50 {
			notes = notes[len(notes)-50:]
		}
		for _, nt := range notes {
			if nt["system"] == true || orStr(nt["id"]) == "" {
				continue
			}
			nid := fmt.Sprintf("gl:%d:%s", pid, pyStr(nt["id"]))
			if r.one(`SELECT 1 FROM comments WHERE jira_id=?`, nid) != nil {
				continue
			}
			a := asMap(nt["author"])
			who := orDefaultS(orStr(a["name"]), orDefaultS(orStr(a["username"]), "?"))
			at := glTS(nt["created_at"])
			if at == 0 {
				at = r.now()
			}
			r.must(`INSERT INTO comments (task_id, jira_id, author, body, created_at) VALUES (?,?,?,?,?)`, tid, nid, who, head(orStr(nt["body"]), 20000), at)
		}
	}
	return nil
}

type glKey struct{ pid, iid int64 }

func (r *run) glPull(g *GitLab, m *glMeta, full bool) (map[glKey]bool, bool, error) {
	last := r.stateInt("gl_last_pull")
	var scope []string
	for _, x := range m.BoardScopeLabels {
		if n := orStr(x["name"]); n != "" {
			scope = append(scope, pyStr(x["name"]))
		}
	}
	started := r.now()
	after := ""
	if !full && last != 0 {
		after = glISO(last - 120)
	}
	cap := 1000
	if full {
		cap = 5000
	}
	var issues []map[string]any
	var err error
	if m.group() {
		issues, err = g.GroupIssues(r.ctx, m.GroupID, after, scope, cap)
	} else {
		issues, err = g.Issues(r.ctx, m.ProjectID, after, scope, cap)
	}
	seen := map[glKey]bool{}
	if err != nil {
		return seen, false, err
	}
	for _, is := range issues {
		pid := m.ProjectID
		if m.group() {
			pid = idOf(is["project_id"])
		}
		seen[glKey{pid, idOf(is["iid"])}] = true
		if err := r.glUpsert(g, m, is, !full); err != nil {
			return seen, false, err
		}
	}
	r.setState("gl_last_pull", strconv.FormatInt(started, 10))
	if full {
		r.setState("gl_last_full", strconv.FormatInt(started, 10))
	}
	r.commit()
	return seen, len(issues) < cap, nil
}

func (r *run) glFullDue() bool {
	if r.getState("gl_want_full", "") == "1" {
		return true
	}
	last := r.stateInt("gl_last_full")
	h := r.o.Now().Hour()
	return last == 0 || (r.now()-last > 20*3600 && h >= 2 && h < 6) || r.now()-last > 48*3600
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// glSync is one GitLab run: push the queue, refresh the board lists, pull. It returns the run record.
func (r *run) glSync() object {
	m := r.o.glMetaRead()
	at := r.now()
	if !r.o.glConnected(m) {
		e := ""
		if r.one(`SELECT 1 FROM tasks WHERE tracker='gitlab' LIMIT 1`) != nil {
			e = "GitLab issues is not connected: GitLab tasks stay on the board, their changes wait"
		}
		return object{{"at", at}, {"ok", false}, {"error", e}, {"counts", object{}}, {"calls", int64(0)}}
	}
	before := map[string]int{}
	for k, v := range r.stats {
		before[k] = v
	}
	var g *GitLab
	errText, ok := "", false
	func() {
		defer func() {
			if x := recover(); x != nil {
				switch e := x.(type) {
				case refused:
					errText = string(e)
				case glRefusal:
					errText = r.glErrText(e.err)
				default:
					panic(x)
				}
			}
		}()
		g = r.glStored(m, -1, 0)
		if err := r.glRun(g, m); err != nil {
			errText = r.glErrText(mustGL(err))
			return
		}
		ok = true
		r.setState("gl_last_ok", strconv.FormatInt(r.now(), 10))
	}()
	r.commit()
	counts := object{}
	for _, k := range []string{"pushed", "failed", "conflicts", "new", "updated", "gone"} {
		if d := r.stats[k] - before[k]; d != 0 {
			counts = append(counts, kv{k, int64(d)})
		}
	}
	calls := int64(0)
	if g != nil {
		calls = int64(g.Calls)
	}
	if errText != "" {
		r.log().Warn("gitlab issues", "error", head(errText, 200))
	}
	return object{{"at", at}, {"ok", ok}, {"error", head(errText, 400)}, {"counts", counts}, {"calls", calls}}
}

func (r *run) glErrText(e *GitLabError) string {
	if e.Kind == "auth" {
		return e.Msg + ". Create a new token and connect again (Settings → Integrations → GitLab issues)."
	}
	return head(e.Msg, 300)
}

func (r *run) glRun(g *GitLab, m *glMeta) error {
	if _, err := g.User(r.ctx); err != nil {
		return err
	}
	if m.BoardID != 0 {
		var b map[string]any
		var err error
		if m.group() {
			b, err = g.GroupBoard(r.ctx, m.GroupID, m.BoardID)
		} else {
			b, err = g.Board(r.ctx, m.ProjectID, m.BoardID)
		}
		if err != nil {
			return err
		}
		lists := glBoardLists(b, m.ListMap)
		scope := []map[string]any{}
		if l, ok := b["labels"].([]any); ok {
			for _, x := range l {
				if mm, ok := x.(map[string]any); ok {
					scope = append(scope, mm)
				}
			}
		}
		old := m.BoardScopeLabels
		if old == nil {
			old = []map[string]any{}
		}
		if !sameJSON(lists, m.Lists) || !sameJSON(scope, old) {
			m.Lists, m.BoardScopeLabels = lists, scope
			r.glSaveMeta(m)
		}
	}
	if err := r.glPush(g, m); err != nil {
		return err
	}
	full := r.glFullDue()
	seen, complete, err := r.glPull(g, m, full)
	if err != nil {
		return err
	}
	if full && complete {
		for _, t := range r.rows(`SELECT id, gl_project, gl_iid FROM tasks WHERE tracker='gitlab' AND gl_iid IS NOT NULL AND gone=0`) {
			if !seen[glKey{t.i("gl_project"), t.i("gl_iid")}] {
				r.must(`UPDATE tasks SET gone=1, updated_at=? WHERE id=?`, r.now(), t.i("id"))
				r.event(t.i("id"), "gitlab", "gitlab", "No longer on the connected GitLab board (deleted, moved, or another project "+
					"or group connected); hidden from the board")
				r.stats["gone"]++
			}
		}
	}
	if full {
		r.setState("gl_want_full", "0")
	}
	r.commit()
	if r.one(`SELECT 1 FROM outbox o JOIN tasks t ON t.id=o.task_id WHERE o.state='queued' AND o.next_at<=? AND t.tracker='gitlab'`, r.now()) != nil {
		return r.glPush(g, m)
	}
	return nil
}
