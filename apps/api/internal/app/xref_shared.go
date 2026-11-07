package app

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/py"
)

// Small helpers of views_board and fix_handoff that most pages call; ported here so every group can use them.

// BoardRef is views_board.ref(t): the Jira key, a GitLab issue's #number (project#number on a group board), else SIR-<id>.
func BoardRef(t Row) string {
	if t.Has("tracker") && t.Get("tracker") == "gitlab" && t.T("gl_iid") {
		if t.Has("gl_ref") && t.T("gl_ref") {
			return t.Str("gl_ref") // a group board: "infra#45"
		}
		return "#" + I2S(t.I("gl_iid"))
	}
	if t.T("jira_key") {
		return t.Str("jira_key")
	}
	return "SIR-" + I2S(t.I("id"))
}

// BoardTaskForSource is views_board.task_for_source(kind, sid).
func BoardTaskForSource(kind string, sid any) Row {
	r := BoardBQ("SELECT id, jira_key FROM tasks WHERE source_kind=? AND source_id=? AND gone=0", kind, py.Str(sid))
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

// BoardCreateButton is views_board.create_button(kind, sid, label="Create task", cls="secondary small");
// label/cls "" mean the defaults. sid is an int or a str.
func BoardCreateButton(kind string, sid any, label, cls string) string {
	if label == "" {
		label = "Create task"
	}
	if cls == "" {
		cls = "secondary small"
	}
	if t := BoardTaskForSource(kind, sid); t != nil {
		return `<a class="tasklink" href="/board/task?id=` + I2S(t.I("id")) + `" title="Open the task on the board">` +
			Icon("board") + `<span class="mono">` + E(BoardRef(t)) + `</span></a>`
	}
	q := py.UrlencodeKV("src", kind, "id", py.Str(sid))
	return `<a class="btn ` + cls + `" href="/board/new?` + E(q) + `">` + Icon("board") + E(label) + `</a>`
}

var (
	reProposalItem = regexp.MustCompile(`^(\p{Nd}{1,2})\.` + reWS + `+(.*)$`)
	reStarTick     = regexp.MustCompile("[*`]")
	reH2WS         = regexp.MustCompile(`^##` + reWS + `+`)
)

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r)
}

// fixSectionHeading is re.match(r"##\s+(Fix proposals|Recommended fix|Fixes|Fix)\b", line, re.I).
func fixSectionHeading(line string) bool {
	loc := reH2WS.FindStringIndex(line)
	if loc == nil {
		return false
	}
	rest := line[loc[1]:]
	for _, alt := range []string{"Fix proposals", "Recommended fix", "Fixes", "Fix"} {
		if len(rest) >= len(alt) && strings.EqualFold(rest[:len(alt)], alt) {
			after := rest[len(alt):]
			if after == "" {
				return true
			}
			r, _ := utf8.DecodeRuneInString(after)
			if !isWordRune(r) {
				return true
			}
		}
	}
	return false
}

// BoardProposals is views_board.proposals(content): the numbered items of an RCA's fix section.
func BoardProposals(content string) []Proposal {
	type cur struct {
		n     int64
		title string
		lines []string
	}
	var out []*cur
	var c *cur
	inSec := false
	for _, line := range py.SplitLines(content) {
		if strings.HasPrefix(line, "## ") {
			if inSec {
				break
			}
			inSec = fixSectionHeading(line)
			continue
		}
		if !inSec {
			continue
		}
		if m := reProposalItem.FindStringSubmatch(line); m != nil {
			n, _ := py.ParseInt(m[1])
			t := strings.TrimRight(py.Strip(reStarTick.ReplaceAllString(m[2], "")), ".:")
			c = &cur{n, t, []string{line}}
			out = append(out, c)
		} else if c != nil {
			c.lines = append(c.lines, line)
		}
	}
	res := make([]Proposal, len(out))
	for i, x := range out {
		t := py.Head(x.title, 150)
		if t == "" {
			t = "Fix " + I2S(x.n)
		}
		res[i] = Proposal{N: x.n, Title: t, Text: py.Strip(strings.Join(x.lines, "\n"))}
	}
	return res
}

// FHNormEnv is fix_handoff.norm_env(env).
func FHNormEnv(env string) string {
	e := strings.ToLower(py.Strip(env))
	switch e {
	case "production", "prod":
		return "release"
	case "pre":
		return "pre-release"
	case "stage", "dev":
		return "develop"
	}
	return e
}
