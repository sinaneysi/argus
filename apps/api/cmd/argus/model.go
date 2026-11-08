package main

// model.go: the shared model runtime (internal/model) built from the configuration, and `argus claude run`, one
// call through it (the operator's check that the provider, the login and the guards work).

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/model"
)

// claudeOn is the Claude usage switch of Settings -> Claude: the live copy settings-out/claude.json when it exists
// (unreadable: off), else settings.json's claude.enabled, else on (the same rule as the web app's ClaudeOn).
func claudeOn(c *config.Config) (bool, string) {
	const off = "Claude usage is off (Settings -> Claude)"
	if b, err := os.ReadFile(filepath.Join(c.SettingsOut, "claude.json")); err == nil {
		var d struct {
			Enabled *bool `json:"enabled"`
		}
		if json.Unmarshal(b, &d) != nil || d.Enabled == nil || !*d.Enabled {
			return false, off
		}
		return true, ""
	}
	if b, err := os.ReadFile(filepath.Join(c.SettingsIn, "settings.json")); err == nil {
		var d struct {
			Claude struct {
				Enabled *bool `json:"enabled"`
			} `json:"claude"`
		}
		if json.Unmarshal(b, &d) == nil && d.Claude.Enabled != nil && !*d.Claude.Enabled {
			return false, off
		}
	}
	return true, ""
}

// modelOptions are the runtime's options from the configuration; db is ops.db for the run records (nil: none). The
// api provider takes the address and the key of the first Settings -> Data sources entry of type anthropic.
func modelOptions(c *config.Config, db *sql.DB) model.Options {
	v := func(k string) string { return strings.TrimSpace(c.Values[k].Raw) }
	co := claudeOptions(c)
	budget, err := strconv.ParseFloat(v("ARGUS_MODEL_MAX_BUDGET_USD"), 64)
	if err != nil || budget <= 0 {
		budget = 5
	}
	mins, err := strconv.Atoi(v("ARGUS_MODEL_MAX_MINUTES"))
	if err != nil || mins <= 0 {
		mins = 65
	}
	o := model.Options{Provider: strings.ToLower(v("ARGUS_MODEL_PROVIDER")), ClaudeBin: co.ClaudeBin, EnvFile: co.EnvFile, Key: co.Key,
		Model: v("ARGUS_MODEL"), APIURL: v("ARGUS_MODEL_API_URL"), MaxBudgetUSD: budget, MaxTimeout: time.Duration(mins) * time.Minute,
		Enabled: func() (bool, string) { return claudeOn(c) }, EgressFile: co.EgressFile, EgressCheck: co.EgressCheck,
		EgressURLs: co.EgressURLs, DB: db, LogDir: v("ARGUS_MODEL_LOG_DIR")}
	if o.Provider == "" {
		o.Provider = model.CLI
	}
	if o.Provider == model.API {
		if e := firstSource(c, "anthropic"); e != nil {
			so := sourcesOptions(c)
			o.APIKey = so.Credential(e)
			if e.URL != "" {
				o.APIURL = e.URL
			}
		}
		if o.APIURL == "" {
			o.APIURL = "https://api.anthropic.com"
		}
	}
	return o
}

const claudeRunHelp = `usage: argus claude run [-file PATH] [-json] [-prompt TEXT]

run     one model call through the shared runtime (ARGUS_MODEL_PROVIDER: cli or api), with the writing style, the
        Claude usage switch, the egress guard, the limits and a run record in ARGUS_DB (task "model-check"). Prints
        the result (the token is never printed). Exit 0 answered, 1 failed, 78 skipped (off, guard, not configured).
`

func runClaudeRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("claude run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file (default: ARGUS_CONFIG, else "+config.DefaultFile+")")
	asJSON := fs.Bool("json", false, "the result as JSON")
	prompt := fs.String("prompt", "Reply with the single word OK.", "the prompt")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, ok := loadQuiet(*file, stderr)
	if !ok {
		return 1
	}
	if c.DemoMode {
		fmt.Fprintln(stderr, "argus claude run: disabled in the demo (DEMO_MODE=1)")
		return 1
	}
	db, err := openOpsDB(c)
	if err != nil {
		db = nil
	} else {
		defer db.Close()
	}
	res := model.New(modelOptions(c, db)).Run(context.Background(), model.Request{Task: "model-check", Prompt: *prompt,
		MaxTurns: 1, BudgetUSD: 0.1, MaxTokens: 200, Timeout: 2 * time.Minute})
	if *asJSON {
		res.Text = model.Redact(res.Text, 2000)
		b, _ := json.MarshalIndent(res, "", " ")
		fmt.Fprintln(stdout, string(b))
	} else if res.Skipped != "" {
		fmt.Fprintln(stderr, "skipped:", res.Skipped)
	} else if res.IsError {
		fmt.Fprintln(stderr, "failed:", res.Error)
	} else {
		fmt.Fprintf(stdout, "%s (provider %s, %d ms, $%.4f)\n", model.Redact(res.Text, 300), res.Provider, res.DurationMS, res.CostUSD)
	}
	switch {
	case res.Skipped != "":
		return skipExit
	case res.IsError:
		return 1
	}
	return 0
}
