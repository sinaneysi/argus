package ideas

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/claudegate"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/model"
	"github.com/sinaneysi/argus/internal/style"
)

type runGolden struct {
	T0        int64                      `json:"t0"`
	Seed      []string                   `json:"seed"`
	BoardSeed []string                   `json:"board_seed"`
	Files     map[string]json.RawMessage `json:"files"`
	JobModel  string                     `json:"job_model"`
	Want      struct {
		Due       string            `json:"due"`
		Check     [][3]string       `json:"check"`
		Prompt    string            `json:"prompt"`
		Agent     string            `json:"agent"`
		Workspace map[string]string `json:"workspace"`
		Records   []struct {
			Persona, Trigger, Text, Out string
			Cost                        float64
			IsError                     bool  `json:"is_error"`
			T                           int64 `json:"t"`
		} `json:"records"`
		DB     map[string]any `json:"db"`
		Status map[string]any `json:"status"`
	} `json:"want"`
}

func runSetup(t *testing.T, g *runGolden) (string, *sql.DB, *Options, RunFiles) {
	d := t.TempDir()
	ops, board := filepath.Join(d, "ops.db"), filepath.Join(d, "board.db")
	for _, x := range []struct {
		path, kind string
		seed       []string
	}{{ops, migrate.Ops, g.Seed}, {board, migrate.Board, g.BoardSeed}} {
		if _, err := migrate.ApplyFile(context.Background(), x.path, x.kind); err != nil {
			t.Fatal(err)
		}
		db, _ := sql.Open("sqlite", x.path)
		for _, s := range x.seed {
			if _, err := db.Exec(s); err != nil {
				t.Fatal(s, err)
			}
		}
		db.Close()
	}
	for k, v := range g.Files {
		os.WriteFile(filepath.Join(d, k), v, 0o644)
	}
	etc := filepath.Join(d, "etc")
	os.MkdirAll(etc, 0o755)
	os.WriteFile(filepath.Join(etc, "ideas-job-model.md"), []byte(g.JobModel), 0o644)
	db, _ := sql.Open("sqlite", ops)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	env := map[string]string{"FEATURE_IDEAS": "1", "IDEAS_MAX_PER_RUN": "4"}
	loc, _ := time.LoadLocation("Asia/Tokyo")
	o := &Options{StateDir: d, ActionsDir: filepath.Join(d, "idea-actions"), Env: func(k string) string { return env[k] }, Loc: loc,
		Now: func() time.Time { return time.Unix(g.T0, 0) }}
	return d, db, o, RunFiles{EtcDir: etc, BoardDB: board}
}

func loadRunGolden(t *testing.T) *runGolden {
	b, err := os.ReadFile("testdata/ideas-run-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var g runGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return &g
}

// testdata/ideas-run-parity.json: the reference job (scheduling, the workspace of facts, the validation, dedupe and
// store of canned answers) on synthetic data, with the neutral prompt, persona and feature files of this package;
// the reference's own product name in the workspace text is replaced by the neutral one.
func TestRunParity(t *testing.T) {
	g := loadRunGolden(t)
	d, db, o, f := runSetup(t, g)
	cfg := o.LoadConfig()
	if p, err := o.Due(db, cfg); err != nil || p != g.Want.Due {
		t.Errorf("due %q %v, want %q", p, err, g.Want.Due)
	}
	for _, c := range g.Want.Check {
		if why, err := o.Check(db, cfg, c[0], c[1]); err != nil || why != c[2] {
			t.Errorf("check %s %s: %q %v, want %q", c[0], c[1], why, err, c[2])
		}
	}
	if p := o.RunPrompt(f); p != g.Want.Prompt {
		t.Errorf("prompt differs:\n%s", firstLines(p, g.Want.Prompt))
	}
	ws := filepath.Join(d, "ws")
	agent, err := o.Workspace(db, cfg, f, "noc", ws)
	if err != nil || agent != g.Want.Agent {
		t.Fatalf("workspace %q %v", agent, err)
	}
	for name, want := range g.Want.Workspace {
		got, _ := os.ReadFile(filepath.Join(ws, name))
		if string(got) != want {
			t.Errorf("%s differs:\n got %s", name, firstLines(string(got), want))
		}
	}
	if _, err := os.Stat(filepath.Join(ws, ".claude", "agents", agent+".md")); err != nil {
		t.Error("agent file missing")
	}
	for _, r := range g.Want.Records {
		at := r.T
		o.Now = func() time.Time { return time.Unix(at, 0) }
		res := model.Result{Text: r.Text, CostUSD: r.Cost, IsError: r.IsError, RunID: 7}
		if r.IsError {
			res.Error = "error_max_turns"
		}
		out, err := o.Record(db, cfg, r.Persona, r.Trigger, res)
		if err != nil || out+"\n" != r.Out {
			t.Errorf("record %s: %v\n got %q\nwant %q", r.Persona, err, out+"\n", r.Out)
		}
	}
	got := map[string]any{}
	for _, tb := range []string{"ideas", "idea_events"} {
		rs, err := rows(db, "SELECT * FROM "+tb+" ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		got[tb] = rs
	}
	var st map[string]any
	sb, _ := os.ReadFile(filepath.Join(d, "ideas-status.json"))
	json.Unmarshal(sb, &st)
	delete(st, "at")
	for k, pair := range map[string][2]any{"db": {got, g.Want.DB}, "status": {st, g.Want.Status}} {
		gb, _ := json.Marshal(pair[0])
		wb, _ := json.Marshal(pair[1])
		var gn, wn any
		json.Unmarshal(gb, &gn)
		json.Unmarshal(wb, &wn)
		if !reflect.DeepEqual(gn, wn) {
			a, _ := json.MarshalIndent(gn, "", " ")
			w, _ := json.MarshalIndent(wn, "", " ")
			t.Errorf("%s differs:\n got %s", k, firstLines(string(a), string(w)))
		}
	}
}

// The whole run through the model runtime with a fake CLI: the workspace is the working directory, the adviser
// is the agent, the answer is stored, and a second run the same day is refused.
func TestRunWithFakeCLI(t *testing.T) {
	g := loadRunGolden(t)
	style.Dirs = func() []string { return nil }
	d, db, o, f := runSetup(t, g)
	env := filepath.Join(d, "claude.env")
	os.WriteFile(env, []byte("CLAUDE_CODE_OAUTH_TOKEN=test-login-value\n"), 0o600)
	bin, _ := filepath.Abs("testdata/fake-claude")
	m := model.New(model.Options{Provider: model.CLI, ClaudeBin: bin, EnvFile: env, TempDir: d, DB: db,
		EgressFile: filepath.Join(d, "e.json"), EgressCheck: filepath.Join(d, "c.json"),
		LookupFn: func(map[string]string, []string) claudegate.Found {
			return claudegate.Found{IP: "203.0.113.1", Loc: "DE"}
		}})
	out, err := o.Run(context.Background(), db, m, f, "auto")
	if err != nil || out != "OK: 1 new idea from NOC adviser (noc)" {
		t.Fatalf("%q %v", out, err)
	}
	var status, summary string
	db.QueryRow(`SELECT status, summary FROM runs WHERE task='ideas'`).Scan(&status, &summary)
	if status != "OK" || summary != out {
		t.Errorf("runs row %q %q", status, summary)
	}
	if out, _ := o.Run(context.Background(), db, m, f, "auto"); out != "ideas: nothing due" {
		t.Errorf("second auto run: %q", out)
	}
	if out, _ := o.Run(context.Background(), db, m, f, "qa"); out != "ideas: Generate now was used today for this persona (once a day)" {
		t.Errorf("manual run: %q", out)
	}
}
