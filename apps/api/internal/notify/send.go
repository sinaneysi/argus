package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Sender posts messages. Matrix when Matrix is on (only to Room, never to a watched or alert room); else a Telegram
// bot when its token and chat are set; else a JSON POST {"text": body} to Webhook; else nothing (the caller prints
// the body). Every message is secret-scanned first.
type Sender struct {
	Matrix      bool     // ARGUS_NOTIFY_MATRIX (MATRIX_NOTIFY_ENABLED)
	Homeserver  string   // MATRIX_HOMESERVER
	Token       string   // the Matrix access token (from ARGUS_MATRIX_TOKEN_FILE, else MATRIX_ACCESS_TOKEN)
	Room        string   // MATRIX_NOTIFY_ROOM
	NoSend      []string // rooms never sent to: MATRIX_WATCH_ROOMS, MATRIX_ALERT_ROOMS, MATRIX_KUMA_ROOMS
	Telegram    string   // bot token (TELEGRAM_BOT_TOKEN)
	ChatID      string   // TELEGRAM_CHAT_ID
	Webhook     string   // NOTIFY_WEBHOOK
	Allow       Allowlist
	Client      *http.Client
	Log         *slog.Logger
	telegramAPI string // tests
}

// ErrNotSent means sending is off or not configured; the message was not posted anywhere.
var ErrNotSent = errors.New("not sent")

var txn atomic.Int64

func (s *Sender) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *Sender) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// Safe is the content as it is sent: only our fields, capped, msgtype m.notice unless m.text, secret-scanned.
func (s *Sender) Safe(c Content, label string) Content {
	if c.MsgType != "m.text" {
		c.MsgType = "m.notice"
	}
	c.Body = runeHead(c.Body, 4000)
	if c.Format == "org.matrix.custom.html" && c.FormattedBody != "" {
		c.FormattedBody = runeHead(c.FormattedBody, 12000)
	} else {
		c.Format, c.FormattedBody = "", ""
	}
	c, hits := RedactContent(c, s.Allow)
	if len(hits) > 0 {
		s.log().Warn("secret scan redacted", "label", label, "kinds", HitsLine(hits))
	}
	return c
}

// Send posts one message. It returns ErrNotSent when no channel is configured or Matrix is refused by
// configuration (the room is empty or is a watched room).
func (s *Sender) Send(ctx context.Context, c Content, label string) error {
	c = s.Safe(c, label)
	if strings.TrimSpace(c.Body) == "" {
		return fmt.Errorf("notify: empty message from %s", label)
	}
	switch {
	case s.Matrix:
		return s.matrix(ctx, c)
	case s.Telegram != "" && s.ChatID != "":
		api := s.telegramAPI
		if api == "" {
			api = "https://api.telegram.org"
		}
		form := url.Values{"chat_id": {s.ChatID}, "text": {c.Body}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/bot"+s.Telegram+"/sendMessage", strings.NewReader(form.Encode()))
		if err != nil {
			return errors.New("notify: bad telegram request")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return s.do(req, "telegram")
	case s.Webhook != "":
		b, _ := json.Marshal(map[string]string{"text": c.Body})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Webhook, bytes.NewReader(b))
		if err != nil {
			return errors.New("notify: bad webhook URL")
		}
		req.Header.Set("Content-Type", "application/json")
		return s.do(req, "webhook")
	}
	return ErrNotSent
}

func (s *Sender) do(req *http.Request, what string) error {
	resp, err := s.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // never the URL: a bot token can be in it
		}
		return fmt.Errorf("notify: %s: %v", what, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("notify: %s: HTTP %d", what, resp.StatusCode)
	}
	return nil
}

// RoomAllowed reports why the notify room may not be used ("" when it may).
func (s *Sender) RoomAllowed() string {
	if s.Room == "" {
		return "MATRIX_NOTIFY_ROOM is empty"
	}
	for _, r := range s.NoSend {
		if r == s.Room {
			return "MATRIX_NOTIFY_ROOM is a watched or alert room"
		}
	}
	return ""
}

// threadRootRe is a Matrix event id.
var threadRootRe = regexp.MustCompile(`^\$[A-Za-z0-9_\-+/=.:]{1,250}$`)

// SendThread posts c as a reply in the Matrix thread whose root event is root, in the notify room only. Matrix only:
// it returns ErrNotSent when Matrix is off, the room is refused, or root is not an event id.
func (s *Sender) SendThread(ctx context.Context, c Content, label, root string) error {
	if !s.Matrix {
		return fmt.Errorf("%w: Matrix is off", ErrNotSent)
	}
	if !threadRootRe.MatchString(root) {
		return fmt.Errorf("%w: not a thread root", ErrNotSent)
	}
	c = s.Safe(c, label)
	if strings.TrimSpace(c.Body) == "" {
		return fmt.Errorf("notify: empty message from %s", label)
	}
	return s.matrixRel(ctx, c, map[string]any{"rel_type": "m.thread", "event_id": root, "is_falling_back": true,
		"m.in_reply_to": map[string]any{"event_id": root}})
}

func (s *Sender) matrix(ctx context.Context, c Content) error { return s.matrixRel(ctx, c, nil) }

func (s *Sender) matrixRel(ctx context.Context, c Content, rel map[string]any) error {
	if why := s.RoomAllowed(); why != "" {
		return fmt.Errorf("%w: refusing to send: %s", ErrNotSent, why)
	}
	if s.Homeserver == "" || s.Token == "" {
		return fmt.Errorf("%w: MATRIX_HOMESERVER and a Matrix token must be set", ErrNotSent)
	}
	content := map[string]any{"msgtype": c.MsgType, "body": c.Body, "m.mentions": map[string]any{}}
	if c.Format != "" {
		content["format"], content["formatted_body"] = c.Format, c.FormattedBody
	}
	if rel != nil {
		content["m.relates_to"] = rel
	}
	b, _ := json.Marshal(content)
	id := "argus-" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + strconv.Itoa(os.Getpid()) + "-" +
		strconv.FormatInt(txn.Add(1), 10)
	u := strings.TrimRight(s.Homeserver, "/") + "/_matrix/client/v3/rooms/" + url.PathEscape(s.Room) +
		"/send/m.room.message/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(b))
	if err != nil {
		return errors.New("notify: bad MATRIX_HOMESERVER")
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	return s.do(req, "matrix")
}
