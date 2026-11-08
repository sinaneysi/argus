package model

// cli.go: the cli provider. `claude -p --output-format stream-json --verbose`, the prompt on stdin (a long task file
// does not fit in one argument), a clean environment, a throwaway HOME and CLAUDE_CONFIG_DIR, no session persistence.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// cliEnv is what the Claude CLI may see: PATH, HOME, LANG, TZ, the login and the proxies; nothing of the worker's own
// environment (no data-source credential, no master key).
func cliEnv(env map[string]string, home string) []string {
	out := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "LANG=C.UTF-8", "CLAUDE_CONFIG_DIR=" + home + "/.claude",
		"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	if tz := os.Getenv("TZ"); tz != "" {
		out = append(out, "TZ="+tz)
	}
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
		"https_proxy", "http_proxy", "no_proxy"} {
		if v := env[k]; v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func (r *Runner) cliArgs(req Request) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence",
		"--max-turns", strconv.Itoa(req.MaxTurns), "--max-budget-usd", strconv.FormatFloat(req.BudgetUSD, 'f', 2, 64),
		"--tools", strings.Join(req.Tools, ",")}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if len(req.Disallow) > 0 {
		args = append(args, "--disallowedTools", strings.Join(req.Disallow, ","))
	}
	return args
}

func (r *Runner) runCLI(ctx context.Context, req Request, res *Result) {
	home, err := os.MkdirTemp(r.O.TempDir, "argus-model-")
	if err != nil {
		res.IsError, res.Error = true, "could not start: no temporary folder for the call"
		return
	}
	defer os.RemoveAll(home)
	_ = os.MkdirAll(home+"/.claude", 0o700)
	cmd := exec.CommandContext(ctx, r.O.ClaudeBin, r.cliArgs(req)...)
	cmd.Env = append(cliEnv(r.env(), home), r.O.ExtraEnv...)
	cmd.Dir = home
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // the CLI and what it started
	var stderr bytes.Buffer
	cmd.Stderr = &limited{w: &stderr, n: 16 * 1024}
	out, err := cmd.StdoutPipe()
	if err != nil {
		res.IsError, res.Error = true, "could not start the Claude CLI"
		return
	}
	if err := cmd.Start(); err != nil {
		res.IsError, res.ExitCode = true, 127
		res.Error = "could not start the Claude CLI (" + r.O.ClaudeBin + "): " + errText(err)
		return
	}
	final := readStream(out, req.OnEvent)
	err = cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
	}
	if final != nil {
		res.Text, _ = final["result"].(string)
		res.IsError, _ = final["is_error"].(bool)
		res.CostUSD = num(final["total_cost_usd"])
		res.Turns = int(num(final["num_turns"]))
		res.DurationMS = int64(num(final["duration_ms"]))
		if d, ok := final["permission_denials"].([]any); ok {
			res.Denials = len(d)
		}
		if res.IsError && res.Error == "" {
			res.Error = res.Text
		}
		return
	}
	res.IsError = true
	msg := strings.TrimSpace(stderr.String())
	if msg == "" && err != nil {
		msg = fmt.Sprintf("the Claude CLI ended without a result (exit %d)", res.ExitCode)
	}
	if msg == "" {
		msg = "the Claude CLI ended without a result"
	}
	res.Error = msg
}

func errText(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// readStream reads the stream-json events: OnEvent for the init and each tool call; the final result object back. A
// single pretty-printed document (a CLI that printed plain json) is parsed at the end.
func readStream(src io.Reader, on func(Event)) map[string]any {
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var final map[string]any
	var rest bytes.Buffer
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if json.Unmarshal(line, &ev) != nil {
			if rest.Len() < 256*1024 {
				rest.Write(line)
				rest.WriteByte('\n')
			}
			continue
		}
		switch ev["type"] {
		case "result":
			final = ev
		case "system":
			if ev["subtype"] == "init" && on != nil {
				on(Event{Type: "init"})
			}
		case "assistant":
			msg, _ := ev["message"].(map[string]any)
			content, _ := msg["content"].([]any)
			for _, c := range content {
				cm, _ := c.(map[string]any)
				if cm["type"] != "tool_use" || on == nil {
					continue
				}
				name, _ := cm["name"].(string)
				in, _ := cm["input"].(map[string]any)
				on(Event{Type: "tool", Tool: name, Input: in})
			}
		}
	}
	if final == nil && rest.Len() > 0 {
		var doc map[string]any
		if json.Unmarshal(rest.Bytes(), &doc) == nil && doc["type"] == "result" {
			final = doc
		}
	}
	return final
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}

// limited writes at most n bytes to w and drops the rest (the CLI's stderr is only for the error line).
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := len(p)
		if k > l.n {
			k = l.n
		}
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
