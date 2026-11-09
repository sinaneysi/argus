package board

// jiraconnect.go: the Jira connections of Account (per user) and of Settings -> Jira sync (shared), applied from
// settings-in/jira-connect/<user>-<16 hex>.json at the start of each board sync run. The app writes the request
// once and cannot read it back; this code validates it, calls only a URL that passes the SSRF guard, and keeps
// the secrets where the app does not read them:
//
//	EtcDir/jira/users/<user>.pat   the user's personal access token (0600, sealed with the master key)
//	EtcDir/jira/users/<user>.json  url, mode (ro|rw), projects, Jira identity (no secret)
//	EtcDir/jira/allowed-hosts      the hosts the board may call ("host" or "host:port")
//	EtcDir/jira/shared.json        the shared connection (no secret)
//	PATFile, EnvFiles[0]           the shared token (sealed) and its JIRA_* settings; other lines are kept
//
// Out/jira-users.json is what the app shows (no token). Jira older than 8.14 has no personal access tokens and is
// refused; no password is stored.

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/sinaneysi/argus/internal/secret"
)

const (
	jcMaxFile      = 96 * 1024 // a CA bundle may ride along
	jcUsersFile    = "jira-users.json"
	SharedJiraName = "etc/jira.pat"
)

// UserJiraName is the name a user's Jira token is sealed under.
func UserJiraName(user string) string { return "etc/jira/users/" + user + ".pat" }

var (
	jcUserRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)
	jcTokenRe    = regexp.MustCompile(`^[A-Za-z0-9+/=._-]{10,512}$`)
	jcHostRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*(:[0-9]{1,5})?$`)
	jcProjectRe  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}$`)
	jcPathRe     = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*$`)
	jcApprRe     = regexp.MustCompile(`^[A-Za-z0-9._@-]+(,[A-Za-z0-9._@-]+)*$`)
	jcPemRe      = regexp.MustCompile(`-----BEGIN CERTIFICATE-----\r?\n[A-Za-z0-9+/=\r\n]+-----END CERTIFICATE-----`)
	jcVerRe      = regexp.MustCompile(`^(\d+)\.(\d+)`)
	jcDigitsRe   = regexp.MustCompile(`^[0-9]{1,9}$`)
	jcBoardsRe   = regexp.MustCompile(`/boards/([0-9]{1,9})(?:/|$)`)
	jcSpaceRe    = regexp.MustCompile(`\s+`)
	jcMetadataIP = []string{"169.254.169.254", "100.100.100.200", "fd00:ec2::254", "168.63.129.16"}
)

type jcMeta = map[string]any

func (o *Options) jcEtc() string        { return filepath.Join(o.EtcDir, "jira") }
func (o *Options) jcUsersDir() string   { return filepath.Join(o.jcEtc(), "users") }
func (o *Options) jcReqDir() string     { return o.JiraConnectDir }
func (o *Options) jcSharedMeta() string { return filepath.Join(o.jcEtc(), "shared.json") }
func (o *Options) jcEnvFile() string {
	if len(o.EnvFiles) > 0 {
		return o.EnvFiles[0]
	}
	return filepath.Join(o.EtcDir, "jira.env")
}
func (o *Options) jcCAFile() string { return filepath.Join(filepath.Dir(o.PATFile), "jira-ca.pem") }

func (o *Options) jcCurrentCA() string {
	if fi, err := os.Stat(o.jcCAFile()); err == nil && fi.Mode().IsRegular() {
		return o.jcCAFile()
	}
	return ""
}

func jcReadJSON(path string) jcMeta {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m jcMeta
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func (r *run) jcWrite(path string, data []byte, mode os.FileMode) {
	if r.o.DryRun {
		return
	}
	if err := glWrite(path, data, mode); err != nil {
		panic(refused("could not save (" + strerror(err) + ")"))
	}
}

func (r *run) jcWriteJSON(path string, m jcMeta) {
	b, _ := json.Marshal(m)
	r.jcWrite(path, b, 0o600)
}

// jcSeal seals a token with the master key (plain when no key is configured: the legacy layout).
func (r *run) jcSeal(name, token string) []byte {
	if r.o.Key == nil {
		return []byte(token)
	}
	s, err := r.o.Key.Seal(name, token)
	if err != nil {
		panic(refused("could not seal the token"))
	}
	return []byte(s)
}

// jcReadToken is a token file of this user, mode 0600, opened with the master key when sealed.
func (o *Options) jcReadToken(path, name string) string {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return ""
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(head(string(b), 4096))
	if secret.Sealed(v) {
		plain, err := o.Key.Open(name, v)
		if err != nil {
			return ""
		}
		return plain
	}
	return v
}

func (r *run) jcAudit(line string) { r.glAudit("jira: " + line) }

// --- allow list ------------------------------------------------------------------------------------------------

func (o *Options) jcAllowedHosts() []string {
	out := []string{}
	b, err := os.ReadFile(filepath.Join(o.jcEtc(), "allowed-hosts"))
	if err != nil {
		return out
	}
	for _, x := range splitLines(head(string(b), 65536)) {
		h := strings.ToLower(strings.TrimSpace(x))
		if h != "" && !strings.HasPrefix(h, "#") && jcHostRe.MatchString(h) {
			out = append(out, h)
		}
	}
	return out
}

func (r *run) jcSaveHosts(hosts []string) {
	var b strings.Builder
	for _, h := range hosts {
		b.WriteString(h + "\n")
	}
	if !r.o.DryRun {
		os.MkdirAll(r.o.jcUsersDir(), 0o700)
	}
	r.jcWrite(filepath.Join(r.o.jcEtc(), "allowed-hosts"), []byte(b.String()), 0o644)
}

// --- the SSRF guard --------------------------------------------------------------------------------------------

func jcParseURL(raw string) (string, string, string) {
	if len(raw) > 300 || strings.IndexFunc(raw, isSpace) >= 0 {
		panic(refused("the Jira URL is not valid"))
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil {
		panic(refused("the Jira URL is not valid"))
	}
	if u.Scheme != "https" && !(AllowHTTP && u.Scheme == "http") {
		panic(refused("the Jira URL must start with https://"))
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		panic(refused("the Jira URL must not contain a user name or password"))
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		panic(refused("the Jira URL must not have a query or fragment"))
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		panic(refused("the Jira URL has no host"))
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		panic(refused("the Jira URL has a bad port"))
	}
	if !jcPathRe.MatchString(u.EscapedPath()) {
		panic(refused("the Jira URL path may only hold letters, digits and . _ ~ -"))
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + u.EscapedPath(), host, port
}

func jcBadAddress(ip net.IP) string {
	if AllowHTTP && ip.IsLoopback() {
		return ""
	}
	for _, m := range jcMetadataIP {
		if ip.Equal(net.ParseIP(m)) {
			return "a cloud metadata"
		}
	}
	return badAddress(ip)
}

// jcCheckURL is (base, host key, pinned address, newly allowed) for a URL the board may call, else a refusal. An
// admin's URL whose host is not on the allow list is accepted and added by the caller.
func (r *run) jcCheckURL(raw string, hosts []string, admin bool) (string, string, string, bool) {
	base, host, port := jcParseURL(raw)
	key := host
	if port != "443" {
		key = host + ":" + port
	}
	isNew := false
	if !contains(hosts, key) {
		if port != "443" && !AllowHTTP {
			panic(refused(fmt.Sprintf("port %s is not allowed (only 443, unless an admin allows %s in Allowed Jira hosts)", port, key)))
		}
		if !admin {
			panic(refused(key + " is not an allowed Jira host; ask an admin to add it (Admin > Jira sync > Allowed Jira hosts)"))
		}
		isNew = true
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		if r.o.Resolve == nil {
			r.o.Resolve = func(h string) ([]net.IP, error) { return net.DefaultResolver.LookupIP(r.ctx, "ip", h) }
		}
		var err error
		if ips, err = r.o.Resolve(host); err != nil || len(ips) == 0 {
			panic(refused(host + " does not resolve from this server"))
		}
	}
	for _, ip := range ips {
		if why := jcBadAddress(ip); why != "" {
			panic(refused(fmt.Sprintf("%s resolves to %s address %s; refused", host, why, ip)))
		}
	}
	return base, key, ips[0].String(), isNew
}

// jcClient is a client for one connection test: one retry, ten calls, no gap.
func (r *run) jcClient(base, token, pin, caFile string) *Jira {
	j, err := NewJiraPinned(base, token, caFile, pin)
	if err != nil {
		panic(refused(err.Error()))
	}
	j.Retries, j.Budget, j.MinGap = 1, 10, 0
	return j
}

func jcJiraRefusal(err error) {
	if err == nil {
		return
	}
	if IsAuth(err) {
		panic(refused(err.Error() + ". Check that the whole token was pasted and that it has not expired."))
	}
	panic(refused(err.Error()))
}

func jcVersion(info map[string]any) (int64, int64, bool) {
	if v, ok := info["versionNumbers"].([]any); ok && len(v) >= 2 {
		a, ok1 := asInt(v[0])
		b, ok2 := asInt(v[1])
		if ok1 && ok2 {
			return a, b, true
		}
	}
	if m := jcVerRe.FindStringSubmatch(orStr(info["version"])); m != nil {
		a, _ := strconv.ParseInt(m[1], 10, 64)
		b, _ := strconv.ParseInt(m[2], 10, 64)
		return a, b, true
	}
	return 0, 0, false
}

// jcTest is (server info, myself, projects) with the token, else a refusal with Jira's reason.
func (r *run) jcTest(base, token, pin, caFile string) (map[string]any, map[string]any, []any) {
	j := r.jcClient(base, token, pin, caFile)
	info, err := j.ServerInfo(r.ctx)
	jcJiraRefusal(err)
	if a, b, ok := jcVersion(info); ok && (a < 8 || (a == 8 && b < 14)) {
		panic(refused(fmt.Sprintf("Jira %s is older than 8.14 and has no personal access tokens. %s does not store passwords; "+
			"the board stays local for you until Jira is upgraded.", orStr(info["version"]), r.o.Name)))
	}
	me, err := j.Myself(r.ctx)
	jcJiraRefusal(err)
	ps, err := j.Projects(r.ctx)
	jcJiraRefusal(err)
	projects := []any{}
	for _, p := range ps {
		pm := asMap(p)
		if k := orStr(pm["key"]); jcProjectRe.MatchString(k) && len(projects) < 300 {
			projects = append(projects, jcMeta{"key": k, "name": head(orStr(pm["name"]), 80)})
		}
	}
	return info, me, projects
}

func jcKeys(list any) map[string]bool {
	out := map[string]bool{}
	l, _ := list.([]any)
	for _, p := range l {
		out[orStr(asMap(p)["key"])] = true
	}
	return out
}

func jcName(me map[string]any) string {
	if n := orStr(me["name"]); n != "" {
		return head(n, 64)
	}
	return head(orStr(me["key"]), 64)
}

// --- requests --------------------------------------------------------------------------------------------------

func (r *run) jcHandle(req map[string]any, user, role string) string {
	o := r.o
	metaPath := filepath.Join(o.jcUsersDir(), user+".json")
	switch kind := orStr(req["kind"]); kind {
	case "connect":
		token, _ := req["token"].(string)
		mode, _ := req["mode"].(string)
		if !jcTokenRe.MatchString(token) {
			panic(refused("that does not look like a Jira personal access token"))
		}
		if mode != "ro" && mode != "rw" {
			panic(refused("choose read-only or read and write"))
		}
		hosts := o.jcAllowedHosts()
		raw, _ := req["url"].(string)
		base, key, ip, isNew := r.jcCheckURL(raw, hosts, role == "admin")
		info, me, projects := r.jcTest(base, token, ip, o.jcCurrentCA())
		if !r.o.DryRun {
			os.MkdirAll(o.jcUsersDir(), 0o700)
			os.Chmod(o.jcUsersDir(), 0o700)
		}
		if isNew {
			r.jcSaveHosts(append(hosts, key))
			r.jcAudit(fmt.Sprintf("allowed host %s added by %s (own connection)", key, user))
		}
		old := jcReadJSON(metaPath)
		keep := []any{}
		if old != nil && orStr(old["url"]) == base {
			avail := jcKeys(projects)
			prev, _ := old["projects"].([]any)
			for _, p := range prev {
				if avail[orStr(p)] {
					keep = append(keep, p)
				}
			}
		}
		dp := ""
		if old != nil && contains(anyStrings(keep), orStr(old["default_project"])) {
			dp = orStr(old["default_project"])
		} else if len(keep) > 0 {
			dp = orStr(keep[0])
		}
		meta := jcMeta{"url": base, "host": key, "mode": mode, "projects": keep, "default_project": dp,
			"jira_name": jcName(me), "display": head(orStr(me["displayName"]), 80), "version": head(orStr(info["version"]), 20),
			"server_title": head(orStr(info["serverTitle"]), 80), "deployment": head(orStr(info["deploymentType"]), 20),
			"available": projects, "connected_at": r.now()}
		r.jcWrite(filepath.Join(o.jcUsersDir(), user+".pat"), r.jcSeal(UserJiraName(user), token), 0o600)
		r.jcWriteJSON(metaPath, meta)
		r.jcAudit(fmt.Sprintf("%s connected to %s as %s (%s)", user, key, meta["jira_name"], mode))
		return "connected as " + orDefaultS(orStr(meta["display"]), orStr(meta["jira_name"]))
	case "disconnect":
		if !r.o.DryRun {
			os.Remove(filepath.Join(o.jcUsersDir(), user+".pat"))
			os.Remove(metaPath)
		}
		r.jcAudit(user + " disconnected")
		return "disconnected"
	case "projects":
		meta := jcReadJSON(metaPath)
		if meta == nil {
			panic(refused("connect Jira first"))
		}
		avail := jcKeys(meta["available"])
		sel, ok := req["projects"].([]any)
		if ok && len(sel) > 50 {
			ok = false
		}
		for _, p := range sel {
			if s, isS := p.(string); !isS || !avail[s] {
				ok = false
			}
		}
		if !ok {
			panic(refused("choose projects from the list"))
		}
		dp := orStr(req["default_project"])
		uniq := []any{}
		for _, p := range sel {
			if !contains(anyStrings(uniq), p.(string)) {
				uniq = append(uniq, p)
			}
		}
		if dp != "" && !contains(anyStrings(uniq), dp) {
			panic(refused("the project for new tasks must be one of the chosen projects"))
		}
		mode := orStr(meta["mode"])
		if v, has := req["mode"]; has {
			mode, _ = v.(string)
		}
		if mode != "ro" && mode != "rw" {
			panic(refused("choose read-only or read and write"))
		}
		if dp == "" && len(uniq) > 0 {
			dp = orStr(uniq[0])
		}
		meta["projects"], meta["default_project"], meta["mode"] = uniq, dp, mode
		r.jcWriteJSON(metaPath, meta)
		return "saved"
	case "shared-connect", "shared-settings", "shared-disconnect":
		if role != "admin" {
			panic(refused("only admins can change the shared Jira connection"))
		}
		return r.jcShared(kind, req, user)
	case "hosts":
		if role != "admin" {
			panic(refused("only admins can change the allowed Jira hosts"))
		}
		hs, ok := req["hosts"].([]any)
		if !ok || len(hs) > 20 {
			panic(refused("up to 20 hosts"))
		}
		clean := []string{}
		for _, h := range hs {
			s := strings.ToLower(strings.TrimSpace(fmt.Sprint(h)))
			if !jcHostRe.MatchString(s) {
				panic(refused("not a host name: " + head(s, 60)))
			}
			if !contains(clean, s) {
				clean = append(clean, s)
			}
		}
		r.jcSaveHosts(clean)
		r.jcAudit(fmt.Sprintf("allowed hosts set by %s: %s", user, orDefaultS(strings.Join(clean, ", "), "(none)")))
		return "saved"
	}
	panic(refused("unknown request kind"))
}

func anyStrings(xs []any) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, orStr(x))
	}
	return out
}

// jcUpdateEnv sets (or with "" removes) JIRA_* keys of the shared env file, keeping every other line.
func (r *run) jcUpdateEnv(values [][2]string) {
	path := r.o.jcEnvFile()
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		lines = splitLines(head(string(b), 65536))
	}
	var keep []string
	for _, ln := range lines {
		k, _, _ := strings.Cut(strings.TrimSpace(ln), "=")
		drop := false
		for _, kv := range values {
			drop = drop || strings.TrimSpace(k) == kv[0] && strings.Contains(ln, "=")
		}
		if !drop {
			keep = append(keep, ln)
		}
	}
	for _, kv := range values {
		if kv[1] != "" {
			keep = append(keep, kv[0]+"="+kv[1])
		}
	}
	var b strings.Builder
	for _, ln := range keep {
		b.WriteString(ln + "\n")
	}
	r.jcWrite(path, []byte(b.String()), 0o644)
}

// jcValidatePEM: one or more PEM certificates and nothing else, each one parses.
func jcValidatePEM(text string) string {
	if len(text) > 64*1024 {
		panic(refused("the CA certificate is too large (64 KB at most)"))
	}
	blocks := jcPemRe.FindAllString(text, -1)
	if len(blocks) == 0 || strings.TrimSpace(jcPemRe.ReplaceAllString(text, "")) != "" {
		panic(refused("the CA field must hold PEM certificates only (-----BEGIN CERTIFICATE----- ...), nothing else"))
	}
	for _, bl := range blocks {
		p, _ := pem.Decode([]byte(strings.ReplaceAll(bl, "\r", "")))
		if p == nil {
			panic(refused("the CA certificate does not parse"))
		}
		if _, err := x509.ParseCertificate(p.Bytes); err != nil {
			panic(refused("the CA certificate does not parse (" + head(err.Error(), 80) + ")"))
		}
	}
	return strings.Join(blocks, "\n") + "\n"
}

// jcParseBoard is a board id from "11" or a pasted board URL of the connected Jira; "" when empty.
func jcParseBoard(value any, base string) string {
	v := strings.TrimSpace(orStr(value))
	if n, ok := asInt(value); ok {
		v = strconv.FormatInt(n, 10)
	}
	if v == "" {
		return ""
	}
	if jcDigitsRe.MatchString(v) {
		return v
	}
	if len(v) > 300 || strings.IndexFunc(v, isSpace) >= 0 {
		panic(refused("the board must be a number or a Jira board URL"))
	}
	u, err := url.Parse(v)
	if err != nil || !(u.Scheme == "https" || (AllowHTTP && u.Scheme == "http")) || u.Hostname() == "" {
		panic(refused("the board must be a number or an https:// Jira board URL"))
	}
	b, _ := url.Parse(base)
	if b == nil {
		b = &url.URL{}
	}
	portOf := func(x *url.URL) string { return orDefaultS(x.Port(), "443") }
	if !strings.EqualFold(u.Hostname(), b.Hostname()) || portOf(u) != portOf(b) {
		panic(refused(fmt.Sprintf("that board URL is on %s, but the connected Jira is %s", strings.ToLower(u.Host), orDefaultS(strings.ToLower(b.Host), "(none)"))))
	}
	if q := u.Query().Get("rapidView"); jcDigitsRe.MatchString(q) {
		return q
	}
	if m := jcBoardsRe.FindStringSubmatch(u.Path); m != nil {
		return m[1]
	}
	panic(refused("no board id in that URL (expected ...RapidBoard.jspa?rapidView=11 or .../boards/11)"))
}

func (r *run) jcBoardInfo(j *Jira, bid, project string) (string, []any) {
	name := ""
	if bid != "" {
		b, err := j.Board(r.ctx, bid)
		if err != nil {
			if je, ok := asJira(err); ok && (je.Status == 400 || je.Status == 403 || je.Status == 404) {
				panic(refused("board " + bid + " was not found or is not visible to this token"))
			}
			panic(refused("board " + bid + ": " + err.Error()))
		}
		name = head(orStr(b["name"]), 120)
	}
	boards := []any{}
	if project != "" {
		if bs, err := j.Boards(r.ctx, project); err == nil {
			for _, x := range bs {
				m := asMap(x)
				if id, ok := asInt(m["id"]); ok && id >= 0 && len(boards) < 50 {
					boards = append(boards, jcMeta{"id": id, "name": head(orStr(m["name"]), 120)})
				}
			}
		}
	}
	return name, boards
}

func (r *run) jcComponents(j *Jira, project string) []any {
	out := []any{}
	if project == "" {
		return out
	}
	cs, err := j.Components(r.ctx, project)
	if err != nil {
		return out
	}
	for _, c := range cs {
		if n := orStr(asMap(c)["name"]); n != "" && len(out) < 200 {
			out = append(out, head(n, 80))
		}
	}
	return out
}

func (r *run) jcSharedFields(req map[string]any, avail map[string]bool, meta jcMeta, base string) (string, string, string, string) {
	project := strings.TrimSpace(orStr(req["project"]))
	if project != "" && !avail[project] {
		panic(refused("project " + head(project, 20) + " is not visible to this token"))
	}
	if base == "" {
		base = orStr(meta["url"])
	}
	board := jcParseBoard(req["board_id"], base)
	appr := jcSpaceRe.ReplaceAllString(orStr(req["approvers"]), "")
	if appr != "" && !jcApprRe.MatchString(appr) {
		panic(refused("approvers: a comma list of Jira user names"))
	}
	mode := orStr(req["mode"])
	if mode == "" {
		mode = orDefaultS(orStr(meta["mode"]), "ro")
	}
	if mode != "ro" && mode != "rw" {
		panic(refused("choose read-only or read and write"))
	}
	return project, board, appr, mode
}

func (r *run) jcShared(kind string, req map[string]any, user string) string {
	o := r.o
	meta := jcReadJSON(o.jcSharedMeta())
	if meta == nil {
		meta = jcMeta{}
	}
	switch kind {
	case "shared-disconnect":
		if !o.DryRun {
			os.Remove(o.PATFile)
			os.Remove(o.jcSharedMeta())
		}
		r.jcUpdateEnv([][2]string{{"JIRA_URL", ""}, {"JIRA_PROJECT", ""}, {"JIRA_BOARD_ID", ""}, {"JIRA_FIXER_APPROVERS", ""},
			{"JIRA_MODE", ""}, {"JIRA_COMPONENT", ""}})
		r.jcAudit("shared connection removed by " + user)
		return "shared connection removed"
	case "shared-settings":
		if orStr(meta["url"]) == "" {
			panic(refused("connect the shared Jira first"))
		}
		project, board, appr, mode := r.jcSharedFields(req, jcKeys(meta["available"]), meta, "")
		token := o.jcReadToken(o.PATFile, SharedJiraName)
		if token == "" {
			panic(refused("the shared token is missing; connect again"))
		}
		base, _, ip, _ := r.jcCheckURL(orStr(meta["url"]), o.jcAllowedHosts(), true)
		sc := r.jcClient(base, token, ip, o.jcCurrentCA())
		boardName, boards := r.jcBoardInfo(sc, board, project)
		comps := r.jcComponents(sc, project)
		comp := strings.TrimSpace(orStr(req["component"]))
		if comp != "" && !contains(anyStrings(comps), comp) {
			panic(refused(fmt.Sprintf("component %s is not in project %s", head(comp, 40), orDefaultS(project, "-"))))
		}
		r.jcUpdateEnv([][2]string{{"JIRA_PROJECT", project}, {"JIRA_BOARD_ID", board}, {"JIRA_FIXER_APPROVERS", appr},
			{"JIRA_MODE", mode}, {"JIRA_COMPONENT", comp}})
		upd := jcMeta{"project": project, "board_id": board, "board_name": boardName, "boards": boards,
			"approvers": appr, "mode": mode, "components": comps, "component": comp}
		for k, v := range upd {
			meta[k] = v
		}
		r.jcWriteJSON(o.jcSharedMeta(), meta)
		r.jcAudit(fmt.Sprintf("shared settings by %s: project=%s mode=%s approvers=%s", user, orDefaultS(project, "-"), mode, orDefaultS(appr, "-")))
		if board != "" {
			return "saved; board " + board + " — " + boardName
		}
		return "saved"
	}
	token, _ := req["token"].(string)
	if !jcTokenRe.MatchString(token) {
		panic(refused("that does not look like a Jira personal access token"))
	}
	caFile := ""
	if ca := orStr(req["ca_pem"]); strings.TrimSpace(ca) != "" {
		p := jcValidatePEM(ca)
		caFile = o.jcCAFile() + ".new"
		if err := glWrite(caFile, []byte(p), 0o644); err != nil {
			panic(refused("could not save (" + strerror(err) + ")"))
		}
	}
	hosts := o.jcAllowedHosts()
	var base, key, ip, project, board, appr, mode, boardName string
	var isNew bool
	var info, me map[string]any
	var projects, boards []any
	func() {
		defer func() {
			if x := recover(); x != nil {
				if caFile != "" {
					os.Remove(caFile)
				}
				panic(x)
			}
		}()
		raw, _ := req["url"].(string)
		base, key, ip, isNew = r.jcCheckURL(raw, hosts, true)
		info, me, projects = r.jcTest(base, token, ip, orDefaultS(caFile, o.jcCurrentCA()))
		project, board, appr, mode = r.jcSharedFields(req, jcKeys(projects), meta, base)
		boardName, boards = r.jcBoardInfo(r.jcClient(base, token, ip, orDefaultS(caFile, o.jcCurrentCA())), board, project)
	}()
	if !o.DryRun {
		os.MkdirAll(o.jcUsersDir(), 0o700)
	}
	if caFile != "" {
		if o.DryRun {
			os.Remove(caFile)
		} else {
			os.Rename(caFile, o.jcCAFile())
		}
		r.jcAudit("internal CA for Jira uploaded by " + user)
	}
	if isNew {
		r.jcSaveHosts(append(hosts, key))
		r.jcAudit(fmt.Sprintf("allowed host %s added by %s (shared connection)", key, user))
	}
	r.jcWrite(o.PATFile, r.jcSeal(SharedJiraName, token), 0o600)
	r.jcUpdateEnv([][2]string{{"JIRA_URL", base}, {"JIRA_PROJECT", project}, {"JIRA_BOARD_ID", board},
		{"JIRA_FIXER_APPROVERS", appr}, {"JIRA_MODE", mode}, {"JIRA_CA_FILE", o.jcCurrentCA()}})
	meta = jcMeta{"url": base, "host": key, "mode": mode, "project": project, "board_id": board, "board_name": boardName,
		"boards": boards, "approvers": appr, "jira_name": jcName(me), "display": head(orStr(me["displayName"]), 80),
		"version": head(orStr(info["version"]), 20), "deployment": head(orStr(info["deploymentType"]), 20),
		"available": projects, "connected_at": r.now(), "by": user}
	r.jcWriteJSON(o.jcSharedMeta(), meta)
	r.jcAudit(fmt.Sprintf("shared connection to %s as %s (%s) saved by %s", key, meta["jira_name"], mode, user))
	res := "connected as " + orDefaultS(orStr(meta["display"]), orStr(meta["jira_name"]))
	if project == "" {
		res += "; now choose the project"
	}
	return res
}

func (r *run) jcSafeHandle(req map[string]any, user, role string) (res string) {
	defer func() {
		if x := recover(); x != nil {
			if e, ok := x.(refused); ok {
				res = "refused: " + string(e)
				return
			}
			panic(x)
		}
	}()
	return r.jcHandle(req, user, role)
}

// jcProcess consumes every request file of JiraConnectDir; the results go to r.jcResults.
func (r *run) jcProcess() {
	if r.o.JiraConnectDir == "" || r.o.EtcDir == "" {
		return
	}
	ents, err := os.ReadDir(r.o.jcReqDir())
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(r.o.jcReqDir(), name)
		mm := glNameRe.FindStringSubmatch(name)
		if mm == nil {
			if !strings.HasPrefix(name, ".") && !r.o.DryRun {
				os.Remove(path)
			}
			continue
		}
		user, rid := mm[1], mm[2]
		req, rerr := jcReadRequest(path)
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
				result = r.jcSafeHandle(req, user, role)
			}
		}
		if !r.o.DryRun {
			os.Remove(path)
		}
		if token != "" {
			result = strings.ReplaceAll(result, token, "[REDACTED]")
		}
		r.jcResults[rid] = jcMeta{"user": user, "kind": head(orStr(req["kind"]), 20), "result": head(result, 400), "at": r.now()}
		r.log().Info("jira-connect request", "id", rid, "kind", orDefaultS(orStr(req["kind"]), "?"), "by", user, "result", head(result, 200))
	}
}

func jcReadRequest(path string) (map[string]any, error) {
	return readRequestFile(path, jcMaxFile)
}

// jcConnections are the stored per-user connections (meta + user), without the token.
func (o *Options) jcConnections() map[string]jcMeta {
	out := map[string]jcMeta{}
	ents, err := os.ReadDir(o.jcUsersDir())
	if err != nil {
		return out
	}
	for _, e := range ents {
		user, ok := strings.CutSuffix(e.Name(), ".pat")
		if !ok || !jcUserRe.MatchString(user) {
			continue
		}
		meta := jcReadJSON(filepath.Join(o.jcUsersDir(), user+".json"))
		if meta == nil || o.jcReadToken(filepath.Join(o.jcUsersDir(), e.Name()), UserJiraName(user)) == "" {
			continue
		}
		meta["user"] = user
		out[user] = meta
	}
	return out
}

// jcPublish writes Out/jira-users.json: everything the app shows, no token.
func (r *run) jcPublish() error {
	if r.o.JiraConnectDir == "" || r.o.EtcDir == "" || r.o.DryRun {
		return nil
	}
	path := filepath.Join(r.o.Out, jcUsersFile)
	res := map[string]any{}
	if old := jcReadJSON(path); old != nil {
		for k, v := range asMapAny(old["results"]) {
			res[k] = v
		}
	}
	for k, v := range r.jcResults {
		res[k] = v
	}
	keys := make([]string, 0, len(res))
	for k := range res {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 100 {
		keys = keys[len(keys)-100:]
	}
	last := map[string]any{}
	for _, k := range keys {
		last[k] = res[k]
	}
	users := map[string]any{}
	for u, c := range r.o.jcConnections() {
		c["last_sync"], c["last_error"], c["last_error_at"], c["fixer_mode"] = 0, "", 0, ""
		users[u] = c
	}
	shared := jcReadJSON(r.o.jcSharedMeta())
	if shared == nil {
		shared = jcMeta{}
	}
	doc := jcMeta{"updated_at": r.now(), "allowed_hosts": r.o.jcAllowedHosts(), "users": users, "shared": shared,
		"ca_installed": r.o.jcCurrentCA() != "", "results": last}
	b, _ := json.MarshalIndent(doc, "", " ")
	return writeAtomic(path, b, 0o644)
}

func asMapAny(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
