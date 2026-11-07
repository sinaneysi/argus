package app

// views_jboard.py: the board page when a Jira board is configured: an exact mirror of that board (columns, statuses,
// WIP limits, rank order, quick filters from board-status.json and board.db). Moving a card asks root to run the
// Jira transition with the clicker's read-and-write connection.

import (
	"regexp"

	"github.com/sinaneysi/argus/internal/py"
)

var (
	JBoardKeyRe     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}-[0-9]{1,9}\n?$`)
	jboardHiPrio    = []string{"Highest", "High", "Blocker", "Critical"}
	JBoardFixerLabl = "argus-fixer"
)

// JBoardBoard is views_jboard.board(): (board, status) when a Jira board snapshot exists, else (nil, status).
func JBoardBoard() (*py.OMap, *py.OMap) {
	st := BoardStatus()
	b := py.AsMap(st.Get("board"))
	if b != nil && py.Truthy(b.Get("columns")) && py.Truthy(st.Get("board_snap_at")) {
		return b, st
	}
	return nil, st
}

func jboardMyJira(user string) string {
	if cn := JiraConn(user); py.Truthy(cn) && py.Truthy(cn.Get("jira_name")) {
		return py.Str(cn.Get("jira_name"))
	}
	return py.OrS(py.AsMap(BoardStatus().Get("user_map")).Get(user), user)
}

func jboardDefaultMine(user string) bool {
	cn := JiraConn(user)
	shared := py.AsMap(JiraState().Get("shared")).Get("jira_name")
	return py.Truthy(cn) || (py.Truthy(shared) && py.Str(shared) == jboardMyJira(user) && isStr(shared))
}

func isStr(v any) bool { _, ok := v.(string); return ok }

// jboardBase is the base dict of page(): show, qf (list), view.
type jboardBase struct {
	show string
	qf   []string
	view string
}

// jboardQS is _qs(base, **kw): "/board?..." (urlencode doseq) of the non-empty values, else "/board".
func jboardQS(b jboardBase) string {
	var pairs [][2]string
	if b.show != "" {
		pairs = append(pairs, [2]string{"show", b.show})
	}
	for _, q := range b.qf {
		pairs = append(pairs, [2]string{"qf", q})
	}
	if b.view != "" {
		pairs = append(pairs, [2]string{"view", b.view})
	}
	if len(pairs) == 0 {
		return "/board"
	}
	return "/board?" + py.Urlencode(pairs)
}

// JBoardPage is views_jboard.page(qs).
// jboardSelect is the mirror's rows for this query: the view, Only my issues and the quick filters.
func jboardSelect(b *py.OMap, qs py.QS, me string) (rows []Row, mine bool, view string, qfOn []int64, qfs []*py.OMap) {
	show := qs.Get("show")
	mine = show == "mine" || (show == "" && jboardDefaultMine(me))
	btype := py.Str(b.Get("type"))
	view = "board"
	if btype == "scrum" {
		view = "sprint"
		if qs.Get("view") == "backlog" {
			view = "backlog"
		}
	}
	for _, x := range qs.List("qf") {
		if py.IsDigit(x) {
			n, _ := py.ParseInt(x)
			qfOn = append(qfOn, n)
		}
	}
	for _, q := range py.AsList(py.Or(b.Get("quick_filters"), []any{})) {
		if m := py.AsMap(q); m != nil {
			qfs = append(qfs, m)
		}
	}
	rows = BoardBQ("SELECT * FROM board_issues WHERE view=? ORDER BY rank", view)
	if mine {
		jn := jboardMyJira(me)
		var f []Row
		for _, r := range rows {
			if r.Get("assignee") == jn {
				f = append(f, r)
			}
		}
		rows = f
	}
	for _, q := range qfOn {
		keys := map[string]bool{}
		for _, x := range BoardBQ("SELECT key FROM board_qf WHERE qf_id=?", q) {
			keys[x.Str("key")] = true
		}
		var f []Row
		for _, r := range rows {
			if keys[r.Str("key")] {
				f = append(f, r)
			}
		}
		rows = f
	}
	return
}

// JBoardPostMove is views_jboard.post_move(form, sess).
func JBoardPostMove(c *Ctx, form py.QS, sess *Session) (int, string) {
	get := form.Get
	b, _ := JBoardBoard()
	key, col, tr := get("key"), get("col"), get("transition")
	colN, _ := py.ParseInt(col)
	if b == nil || !JBoardKeyRe.MatchString(key) || !py.IsDigit(col) || colN >= int64(len(py.AsList(b.Get("columns")))) || (tr != "" && !py.IsDigit(tr)) {
		return 400, "Pick a column of the board"
	}
	if why := BoardWriteBlock(sess); why != "" {
		return 403, "Not moved: " + why
	}
	p := py.NewOMap("kind", "board-move", "key", key, "col", colN)
	if tr != "" {
		p.Set("transition", tr)
	}
	rid, err := BoardWriteRequest(p, sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	if get("back") == "task" {
		return 303, boardRedirect(form, rid, "agent", "/board/task?key="+key+"&req="+rid+"#agent")
	}
	return 303, boardRedirect(form, rid, "", "/board?req="+rid)
}

// JBoardPostRefresh is views_jboard.post_refresh(form, sess).
func JBoardPostRefresh(c *Ctx, form py.QS, sess *Session) (int, string) {
	rid, err := BoardWriteRequest(py.NewOMap("kind", "sync"), sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "", "/board?req="+rid)
}
