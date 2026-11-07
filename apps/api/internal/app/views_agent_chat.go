package app

// views_agent_chat.go: the task chat (ADR-0013 P2) on a task page Argus works. The thread lives in board.db (root is
// the only writer, bin/task_chat.py); the app reads it and writes one request file per posted message into
// settings-in/task-chat/, which root re-validates (role, size, rate, secret scan) before it stores the message and
// queues one bounded, read-only Claude turn. Server-rendered: post-redirect-get; while a reply is pending the page
// refreshes itself every 5 s and the composer is disabled, otherwise there is no refresh, so typing is never lost.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

const agentChatMax = 4000

// agentAskKeyRe is a board-level "Ask Argus" thread (ADR-0013 P2b): no task behind it. The app makes the key when the
// first message starts a thread (key "new" in the form); root accepts it without an agent_tasks row.
var agentAskKeyRe = regexp.MustCompile(`^new-[0-9a-f]{8}$`)

// agentChatPostRoles: D2, viewers read the chat; operators and admins post (today admins, until operator exists).
var agentChatPostRoles = []string{"operator", "admin"}

// AgentChatOn is TASK_CHAT (Admin -> Task agent) as root publishes it in board-status.json. Off: the composer is
// hidden and the old answers / instructions form returns.
func AgentChatOn() bool {
	st := py.AsMap(py.Or(BoardStatus().Get("agent"), py.NewOMap()))
	return py.AsMap(py.Or(st.Get("settings"), py.NewOMap())).Get("chat") == true
}

func agentChatCanPost(sess *Session) bool { return sess != nil && In(sess.Role, agentChatPostRoles...) }

var agentChatWho = map[string]string{"argus": "Argus", "system": "Argus (system)"}

// AgentPostChat is POST /board/agent/chat (class normal): one message as a request file; root re-checks everything.
func AgentPostChat(c *Ctx, form py.QS, sess *Session) (int, string) {
	if !agentChatCanPost(sess) {
		return 403, "Viewers read the chat; operators and admins post"
	}
	key := form.Get("key")
	fresh := key == "new" // P2b: the first message of a board-level "Ask Argus" thread
	if fresh {
		key = "new-" + auth.TokenHex(4)
	}
	ask := agentAskKeyRe.MatchString(key)
	if !ask && !agentKeyRe.MatchString(key) {
		return 400, "Bad request"
	}
	if !ask && agentRow(key) == nil {
		return 404, "Argus is not working this task"
	}
	if !AgentChatOn() {
		return 409, "The task chat is off"
	}
	text := py.Strip(strings.ReplaceAll(strings.ReplaceAll(form.Get("text"), "\r\n", "\n"), "\r", "\n"))
	if text == "" {
		return 400, "Write a message first"
	}
	if py.Len(text) > agentChatMax {
		return 400, "A message is limited to 4000 characters"
	}
	rid, err := agentChatWrite(py.NewOMap("kind", "chat", "key", key, "text", text), sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	if ask {
		def := "/board/ask?key=" + key + "&req=" + rid + "#chat"
		if fresh { // the return field names the page without the new key
			return 303, def
		}
		return 303, boardRedirect(form, rid, "chat", def)
	}
	return 303, boardRedirect(form, rid, "chat", agentTaskURL(key)+"&req="+rid+"#chat")
}

// agentChatWrite drops settings-in/task-chat/<rid>.json (0600, O_EXCL, renamed in place) for root's board sync.
func agentChatWrite(payload *py.OMap, sess *Session) (string, error) {
	rid := time.Now().Format("20060102150405") + "-" + auth.TokenHex(4)
	d := filepath.Join(SET_IN, "task-chat")
	if err := os.MkdirAll(d, 0o777); err != nil {
		return "", err
	}
	p := payload.Copy()
	p.Set("id", rid)
	p.Set("by", sess.Username)
	p.Set("at", NowInt())
	if err := boardDropExcl(d, rid+".json", []byte(py.Dumps(p, -1))); err != nil {
		return "", err
	}
	logf("task chat message by %s: %s", sess.Username, rid)
	return rid, nil
}
