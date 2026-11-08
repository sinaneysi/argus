package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

const base = "https://argus.example.com"

// synthetic token-shaped values, built from parts so no token literal is in the source
const (
	tokGL = "gl" + "pat" + "-abcdefghijklmnop1234"
	tokMX = "sy" + "t_secretvalue123"
)

var hostile = []string{
	"<script>alert(1)</script>",
	`<a href="javascript:alert(1)">x</a>`,
	"<img src=x onerror=alert(1)>",
	`quote " and ' and & amp`,
	"line1\nline2\r\nline3\x00\x1b[31mred\x07",
	"‮evil override‬ ⁧isolate⁩",
	"שלום עולם, זה מבחן",
	"@room wake up everyone",
	"token: " + tokGL + " and MATRIX_ACCESS_TOKEN=" + tokMX + " Bearer abcdefghijklmnopqrstuv",
	strings.Repeat("A", 50000),
}

var (
	tagRe   = regexp.MustCompile(`<(/?)([a-zA-Z]+)([^>]*)>`)
	attrRe  = regexp.MustCompile(`([a-zA-Z-]+)="([^"]*)"`)
	ctrlRe  = regexp.MustCompile("[\x00-\x08\x0b-\x1f\x7f‪-‮⁦-⁩]")
	roomRe  = regexp.MustCompile(`(?i)@room\b`)
	allowed = map[string]bool{"b": true, "br": true, "ul": true, "li": true, "a": true, "font": true}
)

// checkSafe is the reference test's check_safe: only the allowed tags and attributes, no control or bidi override
// characters, no room ping, no secret, within the caps.
func checkSafe(t *testing.T, m Content) {
	t.Helper()
	if m.MsgType != "m.notice" && m.MsgType != "m.text" || m.Format != "org.matrix.custom.html" {
		t.Fatalf("type/format %q %q", m.MsgType, m.Format)
	}
	for _, tg := range tagRe.FindAllStringSubmatch(m.FormattedBody, -1) {
		if !allowed[tg[2]] {
			t.Fatalf("tag %q in %s", tg[2], m.FormattedBody)
		}
		for _, a := range attrRe.FindAllStringSubmatch(tg[3], -1) {
			switch {
			case tg[2] == "a" && a[1] == "href":
				if !strings.HasPrefix(a[2], base+"/") {
					t.Fatalf("href %q", a[2])
				}
			case tg[2] == "font" && a[1] == "data-mx-color":
				if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(a[2]) {
					t.Fatalf("color %q", a[2])
				}
			default:
				t.Fatalf("attribute %s[%s]", tg[2], a[1])
			}
		}
	}
	for _, v := range []string{m.Body, m.FormattedBody} {
		if strings.Contains(strings.ToLower(m.FormattedBody), "<script") || ctrlRe.MatchString(v) || roomRe.MatchString(v) {
			t.Fatalf("unsafe: %q", v[:min(len(v), 300)])
		}
		for _, s := range []string{"abcdefghijklmnop1234", tokMX, "abcdefghijklmnopqrstuv"} {
			if strings.Contains(v, s) {
				t.Fatalf("secret %q in %q", s, v)
			}
		}
	}
	if utf8.RuneCountInString(m.Body) > 4000 || utf8.RuneCountInString(m.FormattedBody) > 12000 {
		t.Fatal("over the caps")
	}
}

func TestCleanHostile(t *testing.T) {
	for _, s := range hostile {
		out := Clean(s, 200)
		if utf8.RuneCountInString(out) > 200 || strings.Contains(out, "\n") || regexp.MustCompile("[\x00-\x1f‪-‮⁦-⁩]").MatchString(out) {
			t.Fatalf("clean(%q) = %q", s[:min(len(s), 40)], out)
		}
	}
	if Escape("<script>") != "&lt;script&gt;" || Escape(`'"&`) != "&#x27;&quot;&amp;" {
		t.Fatal("escape")
	}
	if !strings.Contains(Clean(hostile[6], 200), "שלום") {
		t.Fatal("RTL text must be kept")
	}
	c := Clean(hostile[8], 200)
	if strings.Contains(c, tokGL) || !strings.Contains(c, "MATRIX_ACCESS_TOKEN=[redacted]") {
		t.Fatalf("secret: %q", c)
	}
}

func TestFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"**FAIL**: I wrote the RCA. Then more.":             "I wrote the RCA.",
		"WARN: 3 certs at 1.2 days, host_x ok\nsecond line": "3 certs at 1.2 days, host_x ok",
		"no status word here! and more":                     "no status word here!",
		"ERROR — the run failed? yes":                       "the run failed?",
	} {
		if got := FirstSentence(in, 220); got != want {
			t.Fatalf("FirstSentence(%q) = %q, want %q", in, got, want)
		}
	}
	if utf8.RuneCountInString(FirstSentence("OK: "+strings.Repeat("x", 5000), 220)) > 220 {
		t.Fatal("cap")
	}
}

func TestTextSettingsRatelimit(t *testing.T) {
	f := Formatter{Base: base}
	for _, s := range hostile {
		checkSafe(t, f.Text("setup", "OK: "+s, ""))
		checkSafe(t, f.Settings(s, []string{s, "cert-check enabled"}, ""))
		checkSafe(t, f.Ratelimit(6, 40, []string{s, s, s, s, s, s, s, s}, ""))
	}
	m := f.Text("admin", "FAIL: something broke. more text", "")
	if !strings.HasPrefix(m.Body, "🔴 FAIL · Settings\nSomething broke.") || m.MsgType != "m.text" {
		t.Fatalf("%+v", m)
	}
	if m := f.Text("backup-check", "plain note", ""); m.MsgType != "m.notice" || !strings.Contains(m.Body, base+"/backups") {
		t.Fatalf("%+v", m)
	}
	s := f.Settings("admin", []string{"a", "b", "c", "d", "e", "f", "g"}, "")
	if !strings.Contains(s.Body, "7 settings changed by admin.") || !strings.Contains(s.Body, "• +2 more") {
		t.Fatalf("%s", s.Body)
	}
	one := Message{Level: "OK", Title: "Backups", Facts: []string{"127 sets"}, Link: f.Page("/backups"), Meta: []string{"$0.12"}}.RenderOneLine()
	if one.Body != "🟢 Backups OK · 127 sets · $0.12" || !strings.Contains(one.FormattedBody, `<a href="`+base+`/backups">Backups</a>`) {
		t.Fatalf("%+v", one)
	}
	if f.Page("/incidents/problem", QueryInt{"run", 4}, QueryInt{"idx", 0}) != base+"/incidents/problem?run=4&idx=0" {
		t.Fatal("page")
	}
}

func TestJSONShape(t *testing.T) {
	b, _ := Content{MsgType: "m.notice", Body: "a <b> \"q\" é\n", Format: "f", FormattedBody: "x"}.MarshalJSON()
	if string(b) != `{"msgtype": "m.notice", "body": "a <b> \"q\" é\n", "format": "f", "formatted_body": "x"}` {
		t.Fatal(string(b))
	}
	var c Content
	if err := json.Unmarshal(b, &c); err != nil || c.Body != "a <b> \"q\" é\n" {
		t.Fatal(err, c)
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"password=hunter2222":                               "password=[REDACTED:assignment]",
		"the token: revoked":                                "the token: revoked",
		"NOPASSWD: /usr/bin/systemctl":                      "NOPASSWD: /usr/bin/systemctl",
		"https://user" + ":" + "pw" + "value9@example.com/": "https://user" + ":" + "[REDACTED:url-credentials]@example.com/",
		"api_key = changeme":                                "api_key = changeme",
		tokGL + " rest":                                     "[REDACTED:gitlab-token] rest",
	} {
		if got, _ := Redact(in, nil); got != want {
			t.Fatalf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	if got, h := Redact("password=fixture-value-1", Allowlist{"fixture-value-1"}); got != "password=fixture-value-1" || len(h) != 0 {
		t.Fatal("allowlist", got)
	}
}

func TestSendMatrix(t *testing.T) {
	var got struct {
		path, auth string
		body       map[string]any
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth = r.Method+" "+r.URL.EscapedPath(), r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got.body)
		io.WriteString(w, `{"event_id":"$e"}`)
	}))
	defer ts.Close()
	s := &Sender{Matrix: true, Homeserver: ts.URL, Token: "tkn", Room: "!notify:chat.example", NoSend: []string{"!watch:chat.example"}}
	msg := Formatter{Base: base}.Text("setup", "FAIL: leaked Bearer abcdefghijklmnopqrstuv here. @room", "")
	if err := s.Send(context.Background(), msg, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.path, "PUT /_matrix/client/v3/rooms/") || !strings.Contains(got.path, "/send/m.room.message/argus-") || got.auth != "Bearer tkn" {
		t.Fatalf("%+v", got)
	}
	if got.body["msgtype"] != "m.text" || strings.Contains(got.body["body"].(string), "abcdefghijklmnopqrstuv") {
		t.Fatalf("%+v", got.body)
	}
	if m, ok := got.body["m.mentions"].(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("mentions %+v", got.body)
	}
	// a watched room is refused; off means not sent
	s.Room = "!watch:chat.example"
	if err := s.Send(context.Background(), msg, "test"); !errors.Is(err, ErrNotSent) {
		t.Fatalf("watched room: %v", err)
	}
	if err := (&Sender{}).Send(context.Background(), msg, "test"); !errors.Is(err, ErrNotSent) {
		t.Fatalf("off: %v", err)
	}
	// webhook gets the plain body
	var hook map[string]string
	wh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&hook)
	}))
	defer wh.Close()
	if err := (&Sender{Webhook: wh.URL}).Send(context.Background(), msg, "test"); err != nil || !strings.HasPrefix(hook["text"], "🔴 FAIL · Setup") {
		t.Fatalf("%v %v", err, hook)
	}
}
