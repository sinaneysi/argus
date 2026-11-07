package app

// views_matrix.py: Matrix IDs of Argus users (Account: your own; Users: admins set anyone's), the "from Matrix" card
// badge, and the "Waiting for your OK (Matrix)" previews on the board page. The IDs live in auth.db, table matrix_ids.

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var (
	matrixIDRe   = regexp.MustCompile(`^@[a-z0-9._=/+-]{1,200}:[A-Za-z0-9.-]{1,200}(:[0-9]{1,5})?\n?$`)
	matrixPIDRe  = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{8}\n?$`)
	matrixNameRe = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,64}\n?$`)
)

const (
	matrixSchema     = "CREATE TABLE IF NOT EXISTS matrix_ids (username TEXT PRIMARY KEY, matrix_id TEXT UNIQUE, updated INTEGER)"
	matrixPreviewTTL = 24 * 3600
)

func matrixCon() (*sql.DB, error) {
	c := auth.Con()
	if _, err := c.Exec(matrixSchema); err != nil {
		return nil, err
	}
	return c, nil
}

// matrixAllIDs is views_matrix.all_ids(): {username: matrix id}; {} on any error.
func matrixAllIDs() map[string]string {
	out := map[string]string{}
	c, err := matrixCon()
	if err != nil {
		return out
	}
	rows, err := c.Query("SELECT username, matrix_id FROM matrix_ids")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var u, m sql.NullString
		if rows.Scan(&u, &m) == nil {
			out[u.String] = m.String
		}
	}
	return out
}

// matrixSetID is views_matrix.set_id(user, mxid): "" clears; returns an error text or "".
func matrixSetID(user, mxid string) (string, error) {
	mxid = py.Strip(mxid)
	if mxid != "" && !matrixIDRe.MatchString(mxid) {
		return "A Matrix ID looks like @name:" + MATRIX_SERVER_NAME, nil
	}
	c, err := matrixCon()
	if err != nil {
		return "", err
	}
	if mxid != "" {
		var other string
		if err := c.QueryRow("SELECT username FROM matrix_ids WHERE matrix_id=? AND username!=?", mxid, user).Scan(&other); err == nil {
			return mxid + " already belongs to " + other, nil
		}
		if _, err := c.Exec("INSERT OR REPLACE INTO matrix_ids VALUES (?,?,?)", user, mxid, NowInt()); err != nil {
			return "", err
		}
	} else if _, err := c.Exec("DELETE FROM matrix_ids WHERE username=?", user); err != nil {
		return "", err
	}
	logf("matrix id of %s %s", user, If(mxid != "", "set", "cleared"))
	return "", nil
}

func matrixBotID() string {
	st := ReadJSONMap(filepath.Join(filepath.Dir(DB), "matrix-dm-bot.json"))
	bot := py.OrS(st.Get("bot"), "")
	if matrixIDRe.MatchString(bot) {
		return bot
	}
	return ""
}

// MatrixPostAccount is views_matrix.post_account(form, sess).
func MatrixPostAccount(c *Ctx, form py.QS, sess *Session) (int, string) {
	msg, err := matrixSetID(sess.Username, form.Get("matrix_id"))
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	if msg == "" {
		return 303, "/account?matrix=saved#matrix"
	}
	c.Rej = Reject("matrix", "matrix_id", msg, py.QS{})
	return 400, msg
}

// MatrixBadges is views_matrix.badges(): {jira key: requester} of tasks that came from the Matrix DM.
func MatrixBadges() map[string]any {
	out := map[string]any{}
	for _, r := range BoardBQ("SELECT t.jira_key, m.requester FROM matrix_requests m JOIN tasks t ON t.id=m.task_id " +
		"WHERE t.jira_key IS NOT NULL") {
		out[r.Str("jira_key")] = r.Get("requester")
	}
	return out
}

// MatrixBadge is views_matrix.badge(requester).
func MatrixBadge(requester any) string {
	who := py.OrS(requester, "unknown")
	return `<span class="tag" title="Forwarded to Argus on Matrix; requester ` + E(who) + `">from Matrix: ` + E(py.Head(who, 24)) + `</span>`
}

func matrixPending(user string) []Row {
	return BoardBQ("SELECT * FROM matrix_previews WHERE by=? AND state='pending' AND created>? ORDER BY created DESC "+
		"LIMIT 20", user, NowInt()-matrixPreviewTTL)
}

type matrixChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// matrixWait is one chat request waiting for the admin's OK.
type matrixWait struct {
	PID       string         `json:"pid"`
	Title     string         `json:"title"`
	Requester string         `json:"requester"`
	Forwarded string         `json:"forwarded"`
	Hours     int64          `json:"hours"`
	Project   string         `json:"project"`
	Summary   string         `json:"summary"`
	Choices   []matrixChoice `json:"choices"`
	Assignee  string         `json:"assignee"`
}

// matrixWaiting lists what waits for this admin: the choices keep their order, the assignee first when not among them,
// then Unassigned.
func matrixWaiting(sess *Session) []matrixWait {
	out := []matrixWait{}
	for _, r := range matrixPending(sess.Username) {
		type choice struct{ name, display string }
		var choices []choice
		if v, err := py.ParseJSON([]byte(py.OrS(r.Get("choices"), "[]"))); err == nil {
			for _, cv := range py.AsList(v) {
				cm := py.AsMap(cv)
				if cm == nil {
					panic("bad preview choice")
				}
				if matrixNameRe.MatchString(py.Str(cm.GetD("name", ""))) {
					choices = append(choices, choice{py.Str(cm.Get("name")), py.OrS(cm.Get("display"), py.Str(cm.Get("name")))})
				}
			}
		}
		assignee := r.S("assignee")
		if assignee != "" {
			has := false
			for _, ch := range choices {
				if ch.name == assignee {
					has = true
				}
			}
			if !has {
				choices = append([]choice{{assignee, py.OrS(py.Or(r.Get("assignee_name"), r.Get("assignee")), "")}}, choices...)
			}
		}
		opts := []matrixChoice{}
		for _, ch := range choices {
			opts = append(opts, matrixChoice{ch.name, ch.display + If(r.Get("fwd_name") != nil && ch.name == r.Str("fwd_name"), " (you)", "")})
		}
		opts = append(opts, matrixChoice{"", "Unassigned"})
		left := py.FloorDiv(r.I("created")+matrixPreviewTTL-NowInt()+3599, 3600)
		if left < 1 {
			left = 1
		}
		var sl []string
		for _, ln := range py.SplitLines(r.S("summary")) {
			if py.Strip(ln) != "" {
				sl = append(sl, strings.Join(py.Split(ln), " "))
			}
		}
		req := py.OrS(r.Get("requester"), "unknown")
		req, _, _ = strings.Cut(req, " — ")
		req, _, _ = strings.Cut(req, " (")
		out = append(out, matrixWait{PID: r.Str("pid"), Title: r.Str("title"), Requester: req, Forwarded: BoardShortAgo(r.Get("created")), Hours: left,
			Project: r.Str("project"), Summary: strings.Join(sl, " · "), Choices: opts, Assignee: If(r.Get("assignee") != nil, assignee, "")})
	}
	return out
}

// MatrixPostPreview is views_matrix.post_preview(form, sess).
func MatrixPostPreview(c *Ctx, form py.QS, sess *Session) (int, string) {
	get := form.Get
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	pid, act, a := get("pid"), get("action"), get("assignee")
	if !matrixPIDRe.MatchString(pid) || !In(act, "create", "discard", "assign") || (a != "" && !matrixNameRe.MatchString(a)) {
		return 400, "Bad request"
	}
	found := false
	for _, r := range matrixPending(sess.Username) {
		if r.Str("pid") == pid {
			found = true
		}
	}
	if !found {
		return 404, "That preview is no longer waiting"
	}
	rid, err := BoardWriteRequest(py.NewOMap("kind", "mx-preview", "pid", pid, "action", act, "assignee", a), sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "", "/board?req="+rid)
}
