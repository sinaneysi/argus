// Package model is the one way Argus calls a model (ADR-0021 P3/P4). Every job that asks Claude for text (the task
// agent, RCA, proposals, chat, ideas) calls Runner.Run; nothing else starts a model.
//
// Two providers, chosen by ARGUS_MODEL_PROVIDER:
//
//   - cli (the default): the Claude Code CLI inside the worker container (`claude -p`, stream-json). The login is the
//     token of Settings -> Claude (CLAUDE_CODE_OAUTH_TOKEN in the volume's claude.env, sealed with the master key).
//     Each call gets a throwaway HOME and CLAUDE_CONFIG_DIR under the temporary folder (the root file system may be
//     read-only), a clean environment (PATH, HOME, LANG, TZ, the login, the proxies) and no session persistence.
//   - api: the Anthropic Messages API with the API key of a Settings -> Data sources entry of type anthropic. One
//     request, no tools: the job puts every fact the model needs into the prompt.
//
// Every call, whatever the provider:
//
//  1. builds the prompt with style.Prepend (the writing style of Settings -> Writing style);
//  2. is refused while Settings -> Claude usage is off, and stopped by the egress-country guard (internal/claudegate)
//     with the proxies of the Claude env file, before anything leaves the host (Result.Skipped: a skip, not a failure);
//  3. has a time limit (exit code 124 when it is reached, like timeout(1)), a turn limit and a budget in USD (the CLI's
//     --max-budget-usd; the API: max_tokens);
//  4. keeps secrets out of everything it writes: the error text, the run record and the log are passed through the
//     secret scan of internal/notify;
//  5. is recorded in ops.db `runs` (task, start, duration, status OK/WARN/FAIL/ERROR, summary, cost, turns, denials,
//     is_error, log path), the table the pages already read.
package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/claudegate"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/secret"
	"github.com/sinaneysi/argus/internal/style"
)

// Providers.
const (
	CLI = "cli"
	API = "api"
)

// TimeoutExit is the exit code of a call that reached its time limit (the same as timeout(1)).
const TimeoutExit = 124

// Event is one thing the model did while it works (the CLI's stream-json events; the API sends none).
type Event struct {
	Type  string         // "init" (the CLI started), "tool" (a tool call)
	Tool  string         // the tool's name ("Read", "Bash", ...)
	Input map[string]any // the tool's input: for the progress line only, never stored or logged
}

// Request is one call.
type Request struct {
	Task      string        // the runs.task name of the record ("task-intake", "rca", ...)
	Prompt    string        // without the writing style: Run prepends it
	Model     string        // "": the configured default (CLI: its own default)
	MaxTurns  int           // CLI: --max-turns (0: 30)
	BudgetUSD float64       // CLI: --max-budget-usd; never above Options.MaxBudgetUSD (0: that cap)
	MaxTokens int           // API: max_tokens (0: 16000)
	Timeout   time.Duration // 0: 10 minutes; never above Options.MaxTimeout
	Dir       string        // CLI: the working directory (a workspace the job prepared); "": the throwaway HOME
	Tools     []string      // CLI: the only tools the model gets (--tools); nil: none at all
	Disallow  []string      // CLI: --disallowedTools
	OnEvent   func(Event)   // CLI: called for each event while the model works
}

// Result is what came back.
type Result struct {
	Provider   string  `json:"provider"`
	Text       string  `json:"result"`         // the model's final text (not redacted: the job validates it)
	IsError    bool    `json:"is_error"`       // the model (or the provider) said the call failed
	ExitCode   int     `json:"exit_code"`      // CLI exit code; 124: time limit; API: 0, or the HTTP status on an error
	CostUSD    float64 `json:"total_cost_usd"` // reported (CLI) or estimated from the usage (API)
	Turns      int     `json:"num_turns"`
	DurationMS int64   `json:"duration_ms"`
	Denials    int     `json:"denials"`
	Skipped    string  `json:"skipped,omitempty"` // not started: Claude usage is off, or the egress guard stopped it
	Error      string  `json:"error,omitempty"`   // could not start, or failed: short and redacted
	RunID      int64   `json:"run_id,omitempty"`  // the ops.db runs row (0: not recorded)
	Started    int64   `json:"started"`
}

// Status is the first word of the record: OK, WARN, FAIL from the text, else ERROR or WARN (the reference recorder's
// rule).
func (r Result) Status() string {
	if m := statusRe.FindStringSubmatch(r.Text); m != nil {
		return m[1]
	}
	if r.IsError || r.Text == "" {
		return "ERROR"
	}
	return "WARN"
}

var statusRe = regexp.MustCompile(`^\s*\**\s*(OK|WARN|FAIL)\b`)

// Options say how calls are made. Build them once per job from the configuration (cmd/argus modelOptions).
type Options struct {
	Provider string // cli | api

	// cli
	ClaudeBin string     // the Claude CLI
	EnvFile   string     // KEY=value: CLAUDE_CODE_OAUTH_TOKEN (sealed with Key) and the proxies
	Key       secret.Key // the volume's master key (nil: the token is plaintext, the legacy layout)
	TempDir   string     // where each call's throwaway HOME is made ("": os.TempDir())
	ExtraEnv  []string   // KEY=value added to the CLI's clean environment (tests; never a secret)

	// api
	APIURL     string // e.g. https://api.anthropic.com
	APIKey     string // the anthropic data source's credential
	APIVersion string // the anthropic-version header ("": DefaultAPIVersion)
	Model      string // the default model (both providers; the CLI uses its own when "")

	// limits and guards
	MaxBudgetUSD float64                                            // a call's budget never goes above this (0: 5)
	MaxTimeout   time.Duration                                      // a call's time limit never goes above this (0: 65 minutes)
	Enabled      func() (bool, string)                              // Settings -> Claude usage (nil: on)
	EgressFile   string                                             // the guard's settings
	EgressCheck  string                                             // the guard's cache
	EgressURLs   []string                                           // the guard's lookup sources
	LookupFn     func(map[string]string, []string) claudegate.Found // tests: the egress lookup

	// records
	DB     *sql.DB // ops.db: a runs row per call that started (nil: not recorded)
	LogDir string  // the final result object of each call as JSON, one file per call ("": none)
	Now    func() time.Time
}

// DefaultAPIVersion is the anthropic-version header of the API provider.
const DefaultAPIVersion = "2026-10-21"

// DefaultModel is the API provider's model when none is configured.
const DefaultModel = "claude-opus-5-5"

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Runner makes calls.
type Runner struct{ O Options }

// New is a runner for o.
func New(o Options) *Runner { return &Runner{O: o} }

// Configured says whether the provider has what it needs to start (the reason when not): a token in the Claude env
// file (cli) or an API key and URL (api). It reads nothing secret back.
func (r *Runner) Configured() (bool, string) {
	switch r.O.Provider {
	case API:
		if r.O.APIKey == "" || r.O.APIURL == "" {
			return false, "not configured: connect an Anthropic API key in Settings -> Data sources (type anthropic)"
		}
		return true, ""
	case CLI, "":
		if env := r.env(); env["CLAUDE_CODE_OAUTH_TOKEN"] == "" && env["ANTHROPIC_API_KEY"] == "" {
			return false, "not configured: set the Claude token in Settings -> Claude"
		}
		return true, ""
	}
	return false, "unknown ARGUS_MODEL_PROVIDER " + r.O.Provider + " (cli or api)"
}

// env is the Claude env file's values with the token opened ("" when it cannot be opened).
func (r *Runner) env() map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(r.O.EnvFile)
	if err != nil || len(b) > 64*1024 {
		return out
	}
	for k, v := range parseEnv(string(b)) {
		out[k] = v
	}
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if v, ok := out[k]; ok && secret.Sealed(v) {
			name := secret.ClaudeName
			if k != "CLAUDE_CODE_OAUTH_TOKEN" {
				name = "claude/" + k
			}
			plain, err := r.O.Key.Open(name, v)
			if err != nil {
				plain = ""
			}
			out[k] = plain
		}
	}
	return out
}

// proxies are the env file's proxy variables (the route the Claude traffic takes, for the guard and the API client).
func proxies(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		if strings.HasSuffix(strings.ToUpper(k), "_PROXY") && v != "" {
			out[k] = v
		}
	}
	return out
}

// Guard is (allowed, reason) for a call that would start now: the Claude usage switch, then the egress guard.
func (r *Runner) Guard() (bool, string) {
	if r.O.Enabled != nil {
		if on, why := r.O.Enabled(); !on {
			if why == "" {
				why = "Claude usage is off (Settings -> Claude)"
			}
			return false, why
		}
	}
	g := &claudegate.Gate{SettingsFile: r.O.EgressFile, CacheFile: r.O.EgressCheck, URLs: r.O.EgressURLs,
		Env: proxies(r.env()), Now: r.O.Now, LookupFn: r.O.LookupFn}
	ok, why, _ := g.Guard(false)
	return ok, why
}

// Run makes one call. It never returns an error: a call that could not start has Result.Error (or Skipped).
func (r *Runner) Run(ctx context.Context, req Request) Result {
	start := r.O.now()
	res := Result{Provider: r.O.Provider, Started: start.Unix()}
	if res.Provider == "" {
		res.Provider = CLI
	}
	if ok, why := r.Configured(); !ok {
		res.Skipped = why
		return res
	}
	if ok, why := r.Guard(); !ok {
		res.Skipped = why
		return res
	}
	req = r.limits(req)
	req.Prompt = style.Prepend(req.Prompt)
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	switch res.Provider {
	case API:
		r.runAPI(ctx, req, &res)
	default:
		r.runCLI(ctx, req, &res)
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.ExitCode, res.IsError = TimeoutExit, true
		if res.Error == "" {
			res.Error = fmt.Sprintf("no result within %d s (time limit)", int(req.Timeout.Seconds()))
		}
	}
	if res.DurationMS == 0 {
		res.DurationMS = r.O.now().Sub(start).Milliseconds()
	}
	res.Error = Redact(res.Error, 300)
	r.record(req, &res)
	return res
}

func (r *Runner) limits(req Request) Request {
	maxB := r.O.MaxBudgetUSD
	if maxB <= 0 {
		maxB = 5
	}
	if req.BudgetUSD <= 0 || req.BudgetUSD > maxB {
		req.BudgetUSD = maxB
	}
	maxT := r.O.MaxTimeout
	if maxT <= 0 {
		maxT = 65 * time.Minute
	}
	if req.Timeout <= 0 {
		req.Timeout = 10 * time.Minute
	}
	if req.Timeout > maxT {
		req.Timeout = maxT
	}
	if req.MaxTurns <= 0 {
		req.MaxTurns = 30
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 16000
	}
	if req.Model == "" {
		req.Model = r.O.Model
	}
	if req.Task == "" {
		req.Task = "model"
	}
	return req
}

// Redact is text with every secret replaced, on one line, at most n bytes (0: no limit).
func Redact(text string, n int) string {
	text = strings.ReplaceAll(text, "\x00", "")
	text, _ = notify.Redact(text, nil)
	text = strings.Join(strings.Fields(text), " ")
	if n > 0 && len(text) > n {
		text = strings.ToValidUTF8(text[:n], "")
	}
	return text
}

// record writes the runs row and the log file.
func (r *Runner) record(req Request, res *Result) {
	summary := res.Text
	if summary == "" {
		summary = "ERROR: " + firstNonEmpty(res.Error, "no result")
	}
	summary, _ = notify.Redact(summary, nil)
	if len(summary) > 4000 {
		summary = strings.ToValidUTF8(summary[:4000], "")
	}
	logPath := ""
	if r.O.LogDir != "" && os.MkdirAll(r.O.LogDir, 0o750) == nil {
		logPath = filepath.Join(r.O.LogDir, fmt.Sprintf("%s-%s.json", safeName(req.Task), time.Unix(res.Started, 0).UTC().Format("2006-01-02-150405.000")))
		doc := *res
		doc.Text = summary
		if b, err := json.MarshalIndent(doc, "", " "); err == nil {
			_ = os.WriteFile(logPath, append(b, '\n'), 0o640)
		}
	}
	if r.O.DB == nil {
		return
	}
	isErr := 0
	if res.IsError {
		isErr = 1
	}
	out, err := r.O.DB.Exec(`INSERT INTO runs (task, started_at, duration_ms, status, summary, cost_usd, turns, denials, is_error,
		mr_url, log_path) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, req.Task, res.Started, res.DurationMS, res.Status(), summary,
		res.CostUSD, res.Turns, res.Denials, isErr, mrURL(res.Text), nullStr(logPath))
	if err == nil {
		res.RunID, _ = out.LastInsertId()
	}
}

var (
	mrRe   = regexp.MustCompile(`https://\S+/-/merge_requests/\d+`)
	nameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

func mrURL(s string) any {
	if m := mrRe.FindString(s); m != "" {
		return m
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func safeName(s string) string { return nameRe.ReplaceAllString(s, "-") }

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

var lineRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// parseEnv is the KEY=VALUE lines as a shell would set them (simple quotes stripped): the same as claudetoken.
func parseEnv(text string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(text, "\n") {
		m := lineRe.FindStringSubmatch(strings.TrimRight(l, "\r"))
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
