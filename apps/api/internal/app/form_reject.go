package app

// A rejected save on a settings tab (DESIGN.md "Settings and forms"): the server refused the POST before anything was
// written, so the page is shown again with the posted entries, the field marked (aria-invalid, its message under it)
// and a failed state box that links to it. Several forms share a page, so every field id carries its form's key
// ("f-<form>-<name>"). Secret fields (tokens, keys) are never filled back in: their inputs take no value at all.

import (
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// Rejected is a refused save: which form, the input the message is about, the message and the posted entries.
type Rejected struct {
	Form, Field, Msg string
	Posted           py.QS
}

// Reject builds a Rejected for form/field with msg; posted is the POST body.
func Reject(form, field, msg string, posted py.QS) *Rejected {
	return &Rejected{Form: form, Field: field, Msg: msg, Posted: posted}
}

// FieldID is the id of input name in form ("jira-shared", "url" → "f-jira-shared-url").
func FieldID(form, name string) string {
	return "f-" + form + "-" + strings.NewReplacer(":", "-", "_", "-").Replace(name)
}

// On: this rejection is about form.
func (r *Rejected) On(form string) bool { return r != nil && r.Form == form }

// Val is the posted text after a rejection of form, else the stored value. Never use it for a secret.
func (r *Rejected) Val(form, name, stored string) string {
	if r.On(form) {
		return r.Posted.Get(name)
	}
	return stored
}

// Err is the message under the rejected field ("" elsewhere).
func (r *Rejected) Err(form, name string) string {
	if !r.On(form) || r.Field != name {
		return ""
	}
	return `<span class="f-err" id="` + FieldID(form, name) + `-err">` + E(r.Msg) + `</span>`
}
