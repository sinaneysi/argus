package app

import (
	"net/url"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// A board form carries a hidden "return" field: the page (path?query) it sits on. After the POST the handler
// redirects (303) back there, with ?req=<id> for the banner, so a move on the GitLab tab with filters stays on
// the GitLab tab with the same filters. The value comes from the browser, so it is checked like an open
// redirect: a relative path under one of the board pages only, no scheme, host, "//", "\", dot segment or
// control character, capped in length. Anything else falls back to the handler's default page.

const boardReturnMax = 512

var boardReturnRoots = []string{"/board", "/admin/board", "/proposals", "/admin/lessons", "/review"}

// BoardReturn is the checked return path (path?query, never a fragment, no "req"), or "" when v is not safe.
func BoardReturn(v string) string {
	if v == "" || len(v) > boardReturnMax || v[0] != '/' || strings.HasPrefix(v, "//") || strings.ContainsAny(v, "\\#") {
		return ""
	}
	for _, r := range v {
		if r < 0x21 || r == 0x7f {
			return ""
		}
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	p := u.Path
	if strings.Contains(p, "//") || strings.Contains(p, "/.") || strings.Contains(p, "\\") {
		return ""
	}
	ok := false
	for _, root := range boardReturnRoots {
		if p == root || strings.HasPrefix(p, root+"/") {
			ok = true
		}
	}
	if !ok {
		return ""
	}
	out := u.EscapedPath()
	if keep := py.ParseQS(u.RawQuery).Pairs("req"); len(keep) > 0 {
		out += "?" + py.Urlencode(keep)
	}
	return out
}

// boardReturnInput is the hidden return field for a form on the page at path (c.Path); "" when not a board page.
func boardReturnInput(path string) string {
	r := BoardReturn(path)
	if r == "" {
		return ""
	}
	return `<input type="hidden" name="return" value="` + E(r) + `">`
}

// boardRedirect is where a board POST goes: the form's checked return page plus req=rid (and #anchor), else def.
func boardRedirect(form py.QS, rid, anchor, def string) string {
	r := BoardReturn(form.Get("return"))
	if r == "" {
		return def
	}
	r += If(strings.Contains(r, "?"), "&", "?") + "req=" + url.QueryEscape(rid)
	if anchor != "" {
		r += "#" + anchor
	}
	return r
}
