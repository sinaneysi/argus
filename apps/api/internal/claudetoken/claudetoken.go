// Package claudetoken is the root side of Settings -> Claude: it replaces and tests the Claude token, refreshes the
// egress check and applies the egress guard settings (argus secrets apply-claude, run by a root path unit).
//
// The web app (no access to any secret) writes one request per action into <settings-in>/claude-token/
// <user>-<16 hex>.json (0600):
//
//	{"kind": "replace", "var": "CLAUDE_CODE_OAUTH_TOKEN", "token": <the token>, "save_anyway": false, "user", "at"}
//	{"kind": "test"}            the test call with the token in place
//	{"kind": "egress"}          refresh the egress-country check
//	{"kind": "egress-settings", "guard": true, "blocked": ["IR"], "fail": "closed"}
//
// Apply reads each request once (no symlinks, a regular file written by the app, small), wipes and deletes it, checks
// that the user is an enabled admin, and then for a replace: backs up the env file to <env>.bak-<UTC ts>-claude (same
// owner and mode; the last 5 kept), replaces only that variable's line (temp file + rename, same owner and mode),
// runs ONE cheap test call as the run-as user with the new value, after the egress guard (a token is never tested
// from a blocked egress), keeps the token when the test passed (or hit a usage limit: the login works) or when the
// admin chose "save anyway", and otherwise restores the backup. Status: <settings-out>/claude-token-status.json;
// one audit line per action (who, variable, last 4 characters, result). The token is written nowhere but the env
// file: anything token-like in an error text is masked.
package claudetoken

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/claudegate"

	_ "modernc.org/sqlite"
)

// Var is the only variable the page replaces. An API key is not offered: ANTHROPIC_API_KEY takes precedence over the
// subscription login and is billed per use.
const Var = "CLAUDE_CODE_OAUTH_TOKEN"

var (
	// TokenRe is the shape `claude setup-token` prints (the web app checks the same).
	TokenRe  = regexp.MustCompile(`^sk-ant-oat01-[A-Za-z0-9_-]{20,300}$`)
	maskRe   = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{4,}`)
	limitRe  = regexp.MustCompile(`(?i)usage limit|rate.?limit|limit reached|limit will reset|too many requests|\b429\b`)
	nameRe   = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._@-]{0,63})-([0-9a-f]{16})\.json$`)
	lineRe   = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
	spacesRe = regexp.MustCompile(`\s+`)
)

const (
	keepBackups = 5
	maxFile     = 4096
	testPrompt  = "Reply with the single word OK."
)

// Options say where everything is.
type Options struct {
	ReqDir      string // <settings-in>/claude-token
	OutDir      string // <settings-out>
	EnvFile     string // the env file holding CLAUDE_CODE_OAUTH_TOKEN
	AuthDB      string // users and roles
	RunAs       string // the user the test call runs as ("" or the current user: no switch)
	ClaudeBin   string // the claude CLI
	EgressFile  string // the guard settings (written by egress-settings requests)
	EgressCheck string // the guard's cache, shown by the web app
	EgressURLs  []string
	AppUID      int // the web app's uid (request files must be its or ours)
	TestTimeout time.Duration
	Now         func() time.Time
	LookupFn    func(env map[string]string, urls []string) claudegate.Found // tests
	Log         io.Writer
}

type refused string

func (r refused) Error() string { return string(r) }

// Mask hides anything token-like.
func Mask(s string) string { return maskRe.ReplaceAllString(s, "sk-ant-…") }

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// ParseEnv is the KEY=VALUE lines as a shell would set them (simple quotes stripped).
func ParseEnv(text string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		m := lineRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		v := strings.TrimSpace(m[2])
		if len(v) >= 2 && v[0] == v[len(v)-1] && (v[0] == '\'' || v[0] == '"') {
			v = v[1 : len(v)-1]
		}
		out[m[1]] = v
	}
	return out
}

func last4(v string) string {
	if len(v) >= 12 {
		return v[len(v)-4:]
	}
	return ""
}

// writeLike writes data to path atomically (temp file in the same directory + rename) with like's owner and mode.
func writeLike(path string, data []byte, like string) error {
	st, err := os.Stat(like)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.tmp", filepath.Base(path), os.Getpid()))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if sys, isSys := st.Sys().(*syscall.Stat_t); isSys && os.Geteuid() == 0 {
		if err := f.Chown(int(sys.Uid), int(sys.Gid)); err != nil {
			return err
		}
	}
	if err := f.Chmod(st.Mode().Perm()); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return os.Rename(tmp, path)
}

func (o *Options) backup() (string, error) {
	data, err := os.ReadFile(o.EnvFile)
	if err != nil {
		return "", err
	}
	ts := o.now().UTC().Format("20060102-150405")
	path := o.EnvFile + ".bak-" + ts + "-claude"
	for n := 1; ; n++ {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		path = fmt.Sprintf("%s.bak-%s-%d-claude", o.EnvFile, ts, n)
	}
	if err := writeLike(path, data, o.EnvFile); err != nil {
		return "", err
	}
	olds, _ := filepath.Glob(o.EnvFile + ".bak-*-claude")
	sort.Slice(olds, func(i, j int) bool {
		a, _ := os.Stat(olds[i])
		b, _ := os.Stat(olds[j])
		if a == nil || b == nil || a.ModTime().Equal(b.ModTime()) {
			return olds[i] < olds[j]
		}
		return a.ModTime().Before(b.ModTime())
	})
	for len(olds) > keepBackups {
		os.Remove(olds[0])
		olds = olds[1:]
	}
	return path, nil
}

// replaceVar sets name=value on its own line in place of the existing one(s) (the first keeps its position), at the
// top when missing; every other line stays byte for byte.
func (o *Options) replaceVar(name, value string) error {
	data, err := os.ReadFile(o.EnvFile)
	if err != nil {
		return err
	}
	var out []string
	done := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if line == "" {
			continue
		}
		if m := lineRe.FindStringSubmatch(strings.TrimRight(line, "\n")); m != nil && m[1] == name {
			if !done {
				out = append(out, name+"="+value+"\n")
				done = true
			}
			continue
		}
		out = append(out, line)
	}
	if !done {
		out = append([]string{name + "=" + value + "\n"}, out...)
	}
	return writeLike(o.EnvFile, []byte(strings.Join(out, "")), o.EnvFile)
}

func (o *Options) restore(backup string) error {
	data, err := os.ReadFile(backup)
	if err != nil {
		return err
	}
	return writeLike(o.EnvFile, data, o.EnvFile)
}

// TestResult is the outcome of one test call.
type TestResult struct {
	Result  string `json:"result"` // ok, limit, error, stopped
	Message string `json:"message"`
	At      int64  `json:"at"`
	By      string `json:"by"`
}

// cleanEnv is what the Claude CLI may see: PATH, HOME, LANG, the login and the proxies (the same as the guard uses).
func cleanEnv(env map[string]string, home string) map[string]string {
	out := map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": home, "LANG": "C.UTF-8"}
	for _, k := range []string{"TZ", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
		"https_proxy", "http_proxy", "no_proxy"} {
		if v := env[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

func (o *Options) gate(env map[string]string) *claudegate.Gate {
	return &claudegate.Gate{SettingsFile: o.EgressFile, CacheFile: o.EgressCheck, URLs: o.EgressURLs, Env: env, Now: o.Now,
		LookupFn: o.LookupFn}
}

// testCall is one cheap call as RunAs with the env file's values, after the egress guard.
func (o *Options) testCall() TestResult {
	data, err := os.ReadFile(o.EnvFile)
	if err != nil {
		return TestResult{Result: "error", Message: "cannot read the env file"}
	}
	var cred *syscall.Credential
	home := "/"
	if u, err := user.Current(); err == nil {
		home = u.HomeDir
	}
	if o.RunAs != "" {
		u, err := user.Lookup(o.RunAs)
		if err != nil {
			return TestResult{Result: "error", Message: "user " + o.RunAs + " missing"}
		}
		home = u.HomeDir
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		if os.Geteuid() == 0 && uid != 0 {
			cred = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}
		}
	}
	env := cleanEnv(ParseEnv(string(data)), home)
	if ok, reason, _ := o.gate(env).Guard(true); !ok {
		return TestResult{Result: "stopped", Message: reason}
	}
	timeout := o.TestTimeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.ClaudeBin, "-p", testPrompt, "--max-turns", "1", "--output-format", "json", "--tools", "",
		"--no-session-persistence")
	cmd.Dir = home
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return TestResult{Result: "error", Message: fmt.Sprintf("no answer within %d s", int(timeout.Seconds()))}
	}
	var doc struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
		Type    string `json:"type"`
	}
	parsed := json.Unmarshal(stdout.Bytes(), &doc) == nil && doc.Type != ""
	if err == nil && parsed && !doc.IsError {
		return TestResult{Result: "ok", Message: "Claude answered."}
	}
	msg := doc.Result
	if msg == "" {
		msg = strings.TrimSpace(stderr.String())
	}
	if msg == "" && err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg = fmt.Sprintf("exit %d", ee.ExitCode())
		} else {
			msg = "cannot start claude"
		}
	}
	msg = spacesRe.ReplaceAllString(Mask(strings.TrimSpace(msg)), " ")
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if limitRe.MatchString(msg) {
		return TestResult{Result: "limit", Message: msg}
	}
	return TestResult{Result: "error", Message: msg}
}

func (o *Options) userRole(name string) string {
	db, err := sql.Open("sqlite", "file:"+o.AuthDB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return ""
	}
	defer db.Close()
	var role string
	var disabled int
	if db.QueryRow("SELECT role, disabled FROM users WHERE username=?", name).Scan(&role, &disabled) != nil || disabled != 0 {
		return ""
	}
	return role
}

// readRequest is the request's JSON; the file is wiped and deleted whatever happens.
func (o *Options) readRequest(path string) (map[string]any, error) {
	defer os.Remove(path)
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, refused("cannot open request")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, refused("request is not a regular file")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != o.AppUID && int(sys.Uid) != os.Geteuid() {
		return nil, refused("request not written by the app")
	}
	data, _ := io.ReadAll(io.LimitReader(f, maxFile+1))
	f.WriteAt(make([]byte, len(data)), 0)
	f.Sync()
	if len(data) > maxFile {
		return nil, refused("request file too large")
	}
	var req map[string]any
	if json.Unmarshal(data, &req) != nil || req == nil {
		return nil, refused("request is not a JSON object")
	}
	return req, nil
}

// Status is <settings-out>/claude-token-status.json.
type Status struct {
	Token    map[string]map[string]any `json:"token"`
	Replaced map[string]any            `json:"replaced,omitempty"`
	Test     *TestResult               `json:"test,omitempty"`
	Egress   map[string]any            `json:"egress,omitempty"`
	Results  map[string]map[string]any `json:"results"`
	At       int64                     `json:"at"`
}

func (o *Options) audit(line string) {
	f, err := os.OpenFile(filepath.Join(o.OutDir, "audit.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", o.now().Format("2006-01-02 15:04:05"), Mask(line))
}

func (o *Options) handle(req map[string]any, usr string, st *Status) (string, error) {
	now := o.now().Unix()
	kind, _ := req["kind"].(string)
	switch kind {
	case "egress":
		data, _ := os.ReadFile(o.EnvFile)
		ok, reason, c := o.gate(cleanEnv(ParseEnv(string(data)), "")).Guard(true)
		st.Egress = map[string]any{"at": now, "by": usr}
		if !c.Guard {
			return "egress guard is off", nil
		}
		where := orQ(c.IP) + " in " + orDefault(c.Loc, "unknown")
		if ok {
			return "egress " + where + ": allowed", nil
		}
		return "egress " + where + ": " + reason, nil
	case "egress-settings":
		s := claudegate.Settings{Fail: str(req["fail"])}
		g, isBool := req["guard"].(bool)
		if !isBool {
			return "", refused("guard must be true or false")
		}
		s.Guard = g
		if l, ok := req["blocked"].([]any); ok {
			for _, x := range l {
				s.Blocked = append(s.Blocked, str(x))
			}
		}
		s, err := s.Normalize()
		if err != nil {
			return "", refused(err.Error())
		}
		b, _ := json.Marshal(s)
		for _, p := range []string{o.EgressFile, filepath.Join(o.OutDir, "claude-egress.json")} {
			if err := writeFile(p, append(b, '\n')); err != nil {
				return "", err
			}
		}
		summary := "Claude egress guard off"
		if s.Guard {
			summary = fmt.Sprintf("Claude egress guard on (blocked %s, fail %s)", orDefault(strings.Join(s.Blocked, " "), "none"), s.Fail)
		}
		o.audit(usr + ": " + summary)
		return summary, nil
	case "test":
		t := o.testCall()
		t.At, t.By = now, usr
		st.Test = &t
		data, _ := os.ReadFile(o.EnvFile)
		o.audit(fmt.Sprintf("%s: Claude token test (%s …%s): %s", usr, Var, last4(ParseEnv(string(data))[Var]), t.Result))
		return "test " + t.Result + ": " + t.Message, nil
	case "replace":
	default:
		return "", refused("unknown request kind")
	}
	if v, _ := req["var"].(string); v != Var {
		return "", refused("only " + Var + " can be replaced here")
	}
	token, _ := req["token"].(string)
	if !TokenRe.MatchString(token) {
		return "", refused("that does not look like a Claude OAuth token (sk-ant-oat01-…)")
	}
	anyway := req["save_anyway"] == true
	l4 := last4(token)
	backup, err := o.backup()
	if err != nil {
		return "", err
	}
	if err := o.replaceVar(Var, token); err != nil {
		return "", err
	}
	token, req["token"] = "", nil
	t := o.testCall()
	t.At, t.By = now, usr
	passed := t.Result == "ok" || t.Result == "limit"
	kept := passed || anyway
	if !kept {
		if err := o.restore(backup); err != nil {
			return "", err
		}
	}
	st.Test = &t
	outcome := "kept"
	switch {
	case !kept:
		outcome = "previous token restored"
	case !passed:
		outcome = "kept (save anyway)"
	}
	if kept {
		st.Replaced = map[string]any{"at": now, "by": usr, "var": Var, "last4": l4, "backup": filepath.Base(backup)}
	}
	o.audit(fmt.Sprintf("%s: Claude token %s replaced (…%s); test %s; %s; backup %s", usr, Var, l4, t.Result, outcome, filepath.Base(backup)))
	return fmt.Sprintf("replaced …%s, test %s: %s; %s", l4, t.Result, t.Message, outcome), nil
}

func str(v any) string { s, _ := v.(string); return s }

func orQ(s string) string { return orDefault(s, "?") }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	os.Chmod(tmp, 0o644)
	return os.Rename(tmp, path)
}

// Apply processes the waiting requests and publishes the status. The error is only for the status file.
func Apply(o Options) error {
	if o.Log == nil {
		o.Log = io.Discard
	}
	statusPath := filepath.Join(o.OutDir, "claude-token-status.json")
	var st Status
	if b, err := os.ReadFile(statusPath); err == nil {
		json.Unmarshal(b, &st)
	}
	if st.Results == nil {
		st.Results = map[string]map[string]any{}
	}
	names, _ := os.ReadDir(o.ReqDir)
	for _, de := range names {
		name := de.Name()
		path := filepath.Join(o.ReqDir, name)
		m := nameRe.FindStringSubmatch(name)
		if m == nil {
			if !strings.HasPrefix(name, ".") {
				os.Remove(path)
			}
			continue
		}
		usr, rid := m[1], m[2]
		var result, kind, token string
		req, err := o.readRequest(path)
		if err == nil {
			kind, _ = req["kind"].(string)
			token, _ = req["token"].(string)
			switch {
			case str(req["user"]) != usr:
				err = refused("request user does not match its file name")
			case o.userRole(usr) != "admin":
				err = refused("only an enabled admin can do this")
			default:
				result, err = o.handle(req, usr, &st)
			}
		}
		var r refused
		switch {
		case errors.As(err, &r):
			result = "refused: " + string(r)
		case err != nil:
			result = "refused: could not save (" + err.Error() + ")"
		}
		if token != "" {
			result = strings.ReplaceAll(result, token, "[REDACTED]")
		}
		token = ""
		result = Mask(result)
		if len(result) > 400 {
			result = result[:400]
		}
		if len(kind) > 20 {
			kind = kind[:20]
		}
		st.Results[rid] = map[string]any{"user": usr, "kind": kind, "result": result, "at": o.now().Unix()}
		fmt.Fprintf(o.Log, "claude-token request %s %s by %s: %s\n", rid, kind, usr, firstN(result, 200))
	}
	if len(st.Results) > 20 {
		type kv struct {
			k  string
			at float64
		}
		var all []kv
		for k, v := range st.Results {
			at, _ := v["at"].(float64)
			if i, ok := v["at"].(int64); ok {
				at = float64(i)
			}
			all = append(all, kv{k, at})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].at < all[j].at })
		for _, x := range all[:len(all)-20] {
			delete(st.Results, x.k)
		}
	}
	st.Token = map[string]map[string]any{}
	if data, err := os.ReadFile(o.EnvFile); err == nil {
		env := ParseEnv(string(data))
		for _, k := range []string{Var, "ANTHROPIC_API_KEY"} {
			st.Token[k] = map[string]any{"set": env[k] != "", "last4": last4(env[k])}
		}
	}
	st.At = o.now().Unix()
	b, _ := json.MarshalIndent(st, "", " ")
	return writeFile(statusPath, []byte(Mask(string(b))+"\n"))
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
