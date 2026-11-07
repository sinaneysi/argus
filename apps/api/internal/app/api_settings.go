package app

// What the Settings writes share (ADR-0018): every check stays in the handler; a save it refuses comes back as JSON
// naming the form and the field, with nothing written.

import "github.com/sinaneysi/argus/internal/py"

// settingsPost runs the handler of a write. A secret in the form lives only in this call: a refusal returns the
// message and the field name, never the posted entries.
func (h *reqCtx) settingsPost(route, page string, body map[string]any, sess *Session, run func(*Ctx, string, py.QS) (int, string)) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: page}
	code, out := run(c, route, apiForm(body))
	if c.Rej != nil {
		h.apiFail(&apiErr{400, c.Rej.Msg}, "form", c.Rej.Form, "field", c.Rej.Field)
		return nil, nil
	}
	return apiResult(code, out)
}
