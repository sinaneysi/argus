// Package style is the writing style of every text Argus generates (task descriptions, plans, RCA reports,
// proposals, notices, chat answers). The default ships in the binary (default.md); an admin may replace it in
// Settings -> Writing style. The saved text is applied by the worker (volume layout) or root's `argus settings apply`
// (legacy layout) to ARGUS_ETC_DIR/writing-style.md, with a live copy in ARGUS_SETTINGS_OUT.
//
// REQUIRED: every model run (task agent, RCA, proposals, chat, ideas, ...) builds its prompt with Prepend, so the
// admin's style reaches every generated text.
package style

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/config"
)

//go:embed default.md
var defaultText string

// MaxBytes is the largest style text accepted, in bytes after line ends are normalised (the final newline not counted).
const MaxBytes = 16384

// FileName is the applied style's name in ARGUS_ETC_DIR and its live copy's name in ARGUS_SETTINGS_OUT.
const FileName = "writing-style.md"

// Default is the style shipped in the binary.
func Default() string { return defaultText }

// Normalize gives a text \n line ends, no trailing spaces at the end and one final newline ("" stays "").
func Normalize(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = strings.TrimRight(text, " \t\n")
	text = strings.TrimLeft(text, "\n")
	if text == "" {
		return ""
	}
	return text + "\n"
}

// Validate refuses a text that is not plain UTF-8 text of at most MaxBytes (after Normalize). An empty text is
// valid: it means "use the default".
func Validate(text string) error {
	if !utf8.ValidString(text) {
		return errors.New("the text is not valid UTF-8")
	}
	if n := len(strings.TrimSuffix(Normalize(text), "\n")); n > MaxBytes {
		return fmt.Errorf("the text is %d bytes; the limit is %d (16 KB)", n, MaxBytes)
	}
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return errors.New("the text has control characters; plain text or Markdown only")
		}
	}
	return nil
}

// Dirs gives the folders Current looks in, in order (tests replace it). Default: ARGUS_ETC_DIR, then
// ARGUS_SETTINGS_OUT (the live copy, for a process that cannot read the etc folder).
var Dirs = func() []string {
	var out []string
	for _, k := range []string{"ARGUS_ETC_DIR", "ARGUS_SETTINGS_OUT"} {
		if v, ok := config.Get(k); ok && v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Read is the custom style saved in dir ("", false when there is none or it is not a valid small regular file).
func Read(dir string) (string, bool) {
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	const limit = 2 * MaxBytes // CRLF line ends and trailing space; Validate has the last word
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return "", false
	}
	b, _ := io.ReadAll(io.LimitReader(f, limit+1))
	text := string(b)
	if len(b) > limit || Validate(text) != nil || strings.TrimSpace(text) == "" {
		return "", false
	}
	return Normalize(text), true
}

// CustomIn is the first custom style found in dirs.
func CustomIn(dirs ...string) (string, bool) {
	for _, d := range dirs {
		if t, ok := Read(d); ok {
			return t, true
		}
	}
	return "", false
}

// CurrentIn is the custom style in dirs, else the default.
func CurrentIn(dirs ...string) string {
	if t, ok := CustomIn(dirs...); ok {
		return t
	}
	return defaultText
}

// Current is the style in use: the custom one an admin saved, else the default.
func Current() string { return CurrentIn(Dirs()...) }

// Prepend puts the style in use before a model prompt. Every model run builds its prompt with it.
func Prepend(prompt string) string { return PrependWith(Current(), prompt) }

// PrependWith puts style before prompt, marked as the writing rules for every text the model writes.
func PrependWith(style, prompt string) string {
	return "<writing-style>\nFollow this writing style in every text you write for people.\n\n" + strings.TrimSpace(style) +
		"\n</writing-style>\n\n" + prompt
}
