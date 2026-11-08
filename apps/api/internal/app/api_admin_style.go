package app

// Settings -> Writing style (admins): the style every generated text follows (internal/style). The page reads
// GET /api/v1/settings/writing-style; a save or a reset writes the request SET_IN/writing-style.json, which the worker
// (volume layout) or root's `argus settings apply` (legacy layout) applies within seconds
// (internal/settingsapply/style.go). The style is plain text or Markdown, never a secret.

import (
	"path/filepath"
	"strings"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/py"
	"github.com/sinaneysi/argus/internal/settingsapply"
	"github.com/sinaneysi/argus/internal/style"
)

func styleRequestPath() string { return filepath.Join(SET_IN, settingsapply.StyleRequest) }

// styleDirs: the live copy the applier writes for the app first, then the applied file (when the app can read it).
func styleDirs() []string {
	out := []string{SET_OUT}
	if v, ok := config.Get("ARGUS_ETC_DIR"); ok && v != "" {
		out = append(out, v)
	}
	return out
}

func styleApplier() string {
	if v, _ := config.Get("ARGUS_LAYOUT"); v == "volume" {
		return "the worker"
	}
	return "root's settings apply"
}

// apiWritingStyleJSON is GET /api/v1/settings/writing-style.
func apiWritingStyleJSON(_ py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/admin/writing-style"}
	text, custom := style.CustomIn(styleDirs()...)
	if !custom {
		text = style.Default()
	}
	st := ReadJSONMap(filepath.Join(SET_OUT, settingsapply.StyleStatus))
	notices := ""
	if req := ReadJSONMap(styleRequestPath()); req.Len() > 0 {
		at := py.I(req.Get("updated_at"))
		done := st.Get("ok") == true && py.I(st.Get("request_at")) == at
		if !done && !(st.Get("ok") == false && py.I(st.Get("at")) >= at) {
			notices, _ = requestWaitBanner(c, styleRequestPath(), I2S(at), styleApplier(), "The style in use did not change.", 0)
		}
	}
	if st.Get("ok") == false {
		notices += Banner("Last save was rejected: "+py.Str(st.Get("error"))+" The style in use did not change.", "fail")
	}
	line := "In use: the default style, shipped with Argus."
	if custom {
		line = "In use: a custom style"
		if by := py.Str(st.GetD("by", "")); by != "" && st.Get("custom") == true {
			line += ", saved by " + by + " " + When(st.Get("at"))
		}
		line += "."
	}
	return map[string]any{"text": text, "custom": custom, "line": line, "max": style.MaxBytes, "bytes": len(text),
		"notices": apiNotices(notices), "refresh": apiRefresh(c), "demo": DEMO}, nil
}

// StylePost is POST /api/v1/admin/writing-style: action save (with text) or reset. The message, and the field a
// refusal is about.
func StylePost(form py.QS, sess *Session) (string, string, error) {
	reset := form.Get("action") == "reset"
	if !reset && form.Get("action") != "save" {
		return "Choose save or reset.", "action", nil
	}
	text := ""
	if !reset {
		text = form.Get("text")
		if err := style.Validate(text); err != nil {
			return upperFirst(err.Error()) + ".", "text", nil
		}
		text = style.Normalize(text)
		if text == "" {
			return "The style is empty. Write a style, or use Reset to default.", "text", nil
		}
		if strings.TrimSpace(text) == strings.TrimSpace(style.Default()) {
			reset = true // the default itself: no custom copy to keep in step
			text = ""
		}
	}
	req := py.NewOMap("text", text, "reset", reset, "updated_by", sess.Username, "updated_at", NowInt())
	if err := WriteFileAtomic(styleRequestPath(), filepath.Join(SET_IN, ".writing-style.json.tmp"), []byte(py.Dumps(req, 1))); err != nil {
		return "", "", err
	}
	logf("settings: writing style %s by %s", If(reset, "reset", "saved"), sess.Username)
	return If(reset, "Reset to the default. ", "Saved. ") + "Applying now; reload in a few seconds to see the result.", "", nil
}
