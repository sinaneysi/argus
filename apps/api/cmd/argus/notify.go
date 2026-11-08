package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/collect"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/secret"
	"github.com/sinaneysi/argus/internal/sources"
)

const notifyHelp = `usage: argus notify [-json] <source>                 send stdin as a notice
       argus notify format text <source>              wrap free text from stdin, print the message JSON
       argus notify format settings <by> <change>...  settings-change notice
       argus notify format ratelimit <per-hour> <per-day> [dropped]...

send    stdin is free text (its status word OK/WARN/FAIL/ERROR is the level, its first sentence the summary) or,
        with -json, a message from argus notify format. The message is secret-scanned, then posted to Matrix when
        ARGUS_NOTIFY_MATRIX=1 (only to MATRIX_NOTIFY_ROOM, refused when that is a watched or alert room); else to
        Telegram when TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are set; else to NOTIFY_WEBHOOK; else nowhere. The
        plain-text body is printed in every case. Exit 1 when the message is empty or sending failed.
format  prints one JSON object {"msgtype", "body", "format", "formatted_body"}; links start at ARGUS_PUBLIC_URL.
`

func runNotify(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args) {
		w := stdout
		if len(args) == 0 {
			w = stderr
		}
		fmt.Fprint(w, notifyHelp)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	c, ok := loadQuiet("", stderr)
	if !ok {
		return 1
	}
	notify.SetAppName(c.DisplayName)
	f := notify.Formatter{Base: c.PublicURL}
	if args[0] == "format" {
		msg, ok := formatArgs(f, args[1:], stdin)
		if !ok {
			fmt.Fprint(stderr, notifyHelp)
			return 2
		}
		b, _ := msg.MarshalJSON()
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	asJSON := false
	if args[0] == "-json" || args[0] == "--json" {
		asJSON, args = true, args[1:]
	}
	if len(args) != 1 {
		fmt.Fprint(stderr, notifyHelp)
		return 2
	}
	src := args[0]
	var msg notify.Content
	if asJSON {
		raw, _ := io.ReadAll(io.LimitReader(stdin, 65536))
		if err := json.Unmarshal(raw, &msg); err != nil {
			fmt.Fprintf(stderr, "notify: empty or invalid message from %s\n", src)
			return 1
		}
	} else {
		raw, _ := io.ReadAll(io.LimitReader(stdin, 3500))
		msg = f.Text(src, string(raw), "")
	}
	s := senderFrom(c)
	safe := s.Safe(msg, "notify:"+src)
	if strings.TrimSpace(safe.Body) == "" {
		fmt.Fprintf(stderr, "notify: empty or invalid message from %s\n", src)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := s.Send(ctx, safe, "notify:"+src)
	fmt.Fprintln(stdout, safe.Body)
	if err != nil && !errors.Is(err, notify.ErrNotSent) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err != nil && s.Matrix {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func formatArgs(f notify.Formatter, a []string, stdin io.Reader) (notify.Content, bool) {
	if len(a) == 0 {
		return notify.Content{}, false
	}
	switch {
	case a[0] == "text" && len(a) == 2:
		raw, _ := io.ReadAll(io.LimitReader(stdin, 20000))
		return f.Text(a[1], string(raw), ""), true
	case a[0] == "settings" && len(a) >= 2:
		return f.Settings(a[1], a[2:], ""), true
	case a[0] == "ratelimit" && len(a) >= 3:
		h, err1 := strconv.ParseInt(a[1], 10, 64)
		d, err2 := strconv.ParseInt(a[2], 10, 64)
		if err1 != nil || err2 != nil {
			return notify.Content{}, false
		}
		return f.Ratelimit(h, d, a[3:], ""), true
	}
	return notify.Content{}, false
}

// senderFrom is the notifier of a configuration (the token is read from its file, else the environment key).
func senderFrom(c *config.Config) *notify.Sender {
	v := func(k string) string { return c.Values[k].Raw }
	tok := ""
	p := v("ARGUS_MATRIX_TOKEN_FILE")
	if p == "" {
		p = filepath.Join(v("ARGUS_SECRETS_DIR"), "matrix-token")
	}
	key := volumeKey(c)
	if _, err := os.Lstat(p); err == nil {
		if t, err := key.Open(secret.MatrixName, sources.ReadSecret(p)); err == nil {
			tok = t
		}
	}
	if tok == "" {
		tok = strings.TrimSpace(v("MATRIX_ACCESS_TOKEN"))
	}
	hs := v("MATRIX_HOMESERVER")
	if e := firstSource(c, "matrix"); e != nil { // Settings -> Data sources wins (ADR-0021 D5)
		o := sourcesOptions(c)
		if t := o.Credential(e); t != "" {
			tok, hs = t, e.URL
		}
	}
	var nosend []string
	for _, k := range []string{"MATRIX_WATCH_ROOMS", "MATRIX_ALERT_ROOMS", "MATRIX_KUMA_ROOMS"} {
		nosend = append(nosend, list(c, k)...)
	}
	return &notify.Sender{Matrix: v("ARGUS_NOTIFY_MATRIX") == "1", Homeserver: hs, Token: tok,
		Room: v("MATRIX_NOTIFY_ROOM"), NoSend: nosend, Telegram: v("TELEGRAM_BOT_TOKEN"), ChatID: v("TELEGRAM_CHAT_ID"),
		Webhook: v("NOTIFY_WEBHOOK"), Allow: notify.LoadAllowlist(v("ARGUS_SECRET_ALLOWLIST")), Log: collect.Logger}
}

// settingsNotice posts the settings-change notice (best effort: a failure is logged, the save stays applied).
func settingsNotice(c *config.Config, stderr io.Writer) func(by string, changes []string) {
	return func(by string, changes []string) {
		s := senderFrom(c)
		notify.SetAppName(c.DisplayName)
		msg := notify.Formatter{Base: c.PublicURL}.Settings(by, changes, "")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.Send(ctx, msg, "notify:admin"); err != nil && !errors.Is(err, notify.ErrNotSent) {
			fmt.Fprintln(stderr, "warning: settings notice not sent:", err)
		}
	}
}

// firstSource is the first entry of a type in Settings -> Data sources, by name (nil when there is none).
func firstSource(c *config.Config, typ string) *sources.Entry {
	o := sourcesOptions(c)
	var names []string
	reg := o.Registry()
	for k, e := range reg {
		if e.Type == typ {
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return reg[names[0]]
}

// trackerSource is the first Settings -> Data sources entry of typ with purpose tracker (by key), or nil.
func trackerSource(c *config.Config, typ string) *sources.Entry {
	o := sourcesOptions(c)
	var names []string
	reg := o.Registry()
	for k, e := range reg {
		if e.Type == typ && e.Purpose == "tracker" && e.Managed != "legacy" {
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return reg[names[0]]
}
