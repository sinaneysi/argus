package settingsapply

// Settings -> Writing style. The web app writes the request ARGUS_SETTINGS_IN/writing-style.json:
//
//	{"text": "<markdown>", "reset": false, "updated_by": "<user>", "updated_at": <unix seconds>}
//
// ApplyStyle validates it (a small regular JSON file, known keys only, plain UTF-8 text of at most 16 KB) and writes
// the style to EtcDir/writing-style.md and the live copy OutDir/writing-style.md, or removes both for a reset (the
// default in the binary is used again). Status: OutDir/writing-style-applied.json and one line in audit.log. A
// rejected request keeps what is live. The worker runs it within seconds (volume layout); in the legacy layout root's
// `argus settings apply` does (argus-settings.path also watches the request file).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sinaneysi/argus/internal/style"
)

// StyleRequest is the request file's name in the settings input folder.
const StyleRequest = "writing-style.json"

// StyleStatus is the status file's name in OutDir.
const StyleStatus = "writing-style-applied.json"

// maxStyleRequest bounds the request file: the text's JSON escaping can grow it, never past 8 times.
const maxStyleRequest = 8 * style.MaxBytes

// StyleRequestPath is the request file next to settings.json.
func (o *Options) StyleRequestPath() string { return filepath.Join(filepath.Dir(o.In), StyleRequest) }

type styleReq struct {
	text  string
	reset bool
	by    string
	at    int64
}

func (o *Options) readStyle() (*styleReq, error) {
	f, err := os.OpenFile(o.StyleRequestPath(), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	bad := rej("writing-style.json is not a small regular file")
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxStyleRequest {
		return nil, bad
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStyleRequest+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxStyleRequest {
		return nil, bad
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var raw map[string]any
	if err := d.Decode(&raw); err != nil || d.More() || raw == nil {
		return nil, rej("writing-style.json is not a JSON object")
	}
	r := &styleReq{by: "?"}
	for k, v := range raw {
		switch k {
		case "text":
			s, ok := v.(string)
			if !ok {
				return nil, rej("writing style: text must be a string")
			}
			r.text = s
		case "reset":
			b, ok := v.(bool)
			if !ok {
				return nil, rej("writing style: reset must be true or false")
			}
			r.reset = b
		case "updated_by":
			r.by = head(pyStr(v), 64)
		case "updated_at":
			n, ok := pyInt(v)
			if !ok {
				return nil, rej("writing style: updated_at must be a whole number")
			}
			r.at = n
		default:
			return nil, rej("writing style: unknown setting %s", repr(head(k, 40)))
		}
	}
	if err := style.Validate(r.text); err != nil {
		return nil, rej("writing style: %v", err)
	}
	r.text = style.Normalize(r.text)
	if r.text == "" {
		r.reset = true
	}
	return r, nil
}

// ApplyStyle applies the Settings -> Writing style request. present is false when there is no request file.
func ApplyStyle(o Options) (present bool, err error) {
	o.fill()
	r, err := o.readStyle()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	status := filepath.Join(o.OutDir, StyleStatus)
	if err != nil {
		if !IsRejected(err) {
			return true, err
		}
		msg := head(err.Error(), 300)
		if e := WriteFile(status, dumps(object{{"ok", false}, {"error", msg}, {"at", o.Now().Unix()}})); e != nil {
			return true, e
		}
		o.audit("REJECTED " + msg)
		fmt.Fprintln(o.Err, "rejected:", msg)
		return true, err
	}
	etc, live := filepath.Join(o.EtcDir, style.FileName), filepath.Join(o.OutDir, style.FileName)
	what, changed := "", true
	if r.reset {
		for _, p := range []string{etc, live} {
			if e := os.Remove(p); e != nil && !errors.Is(e, os.ErrNotExist) {
				return true, e
			}
		}
		what = "Writing style reset to the default"
	} else {
		var e error
		if changed, e = replaceIf(etc, []byte(r.text)); e != nil {
			return true, e
		}
		if e := WriteFile(live, []byte(r.text)); e != nil {
			return true, e
		}
		what = fmt.Sprintf("Writing style %s (%d bytes)", map[bool]string{true: "saved", false: "unchanged"}[changed], len(r.text))
	}
	res := object{{"ok", true}, {"at", o.Now().Unix()}, {"by", r.by}, {"request_at", r.at}, {"custom", !r.reset}, {"bytes", len(r.text)}}
	if e := WriteFile(status, dumps(res)); e != nil {
		return true, e
	}
	if e := o.audit(what + " by " + r.by); e != nil {
		return true, e
	}
	fmt.Fprintln(o.Log, strings.ToLower(what[:1])+what[1:])
	if o.Notify != nil && changed {
		o.Notify(r.by, []string{what})
	}
	return true, nil
}
