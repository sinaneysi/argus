package app

// The step-up of a signed-in session as JSON: GET /api/v1/account/stepup says what the Next page must offer, POST takes
// a code and renews the session's 2FA time (the /account/2fa/stepup route of the Go page). A write that answers
// {"mfa": "stepup"} sends the visitor there and back.

import (
	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

func apiStepupJSON(qs py.QS, sess *Session) (any, *apiErr) {
	sensitive := qs.Get("sensitive") == "1"
	back := SafeNext(qs.GetD("next", "/today"))
	pol, mins := PolicyNormal, auth.MFAStepup/60
	if sensitive {
		pol, mins = PolicySensitive, auth.MFASensitive/60
	}
	st := MFAState(sess, pol)
	why := "Changes need a code from the last " + humanMinutes(mins) + "."
	if !auth.MFAStepUpOn {
		why = "Changes need a code once per sign-in; after that this session is not asked again."
	} else if sensitive {
		why = "Settings, users, suppressions, accepted failures and the fixer need a code from the last " + humanMinutes(mins) + "."
	}
	out := map[string]any{"state": st, "why": why, "next": back, "reauth": ""}
	if sess.MFAVia == "idp" && !auth.Enabled(sess.Username) {
		out["reauth"] = "/auth/login?" + py.UrlencodeKV("next", back, "reauth", "1")
	}
	return out, nil
}

// apiStepup checks the code; the new session token goes to the browser in the answer's cookie.
func (h *reqCtx) apiStepup(body map[string]any, sess *Session) (any, *apiErr) {
	via, err := auth.Verify(sess.Username, bodyStr(body, "code"), h.ip())
	if err == auth.ErrNotActive {
		h.apiFail(&apiErr{403, mfaMsg(err)}, "mfa", "enrol")
		return nil, nil
	}
	if err != nil {
		logf("2fa step-up FAILED user=%s ip=%s (api)", sess.Username, h.ip())
		auth.Log(auth.Event{Actor: sess.Username, Action: "mfa.stepup", Target: sess.Username, IP: h.ip(), SID: sess.ID(), Method: "totp", Outcome: "failed"})
		return nil, &apiErr{400, mfaMsg(err)}
	}
	nt := auth.Rotate(h.token(), NowInt(), via)
	ns := auth.GetSession(nt)
	logf("2fa step-up ok user=%s via=%s (api)", sess.Username, via)
	h.apiJSON(200, map[string]any{"ok": true, "redirect": SafeNext(bodyStr(body, "next"))}, [2]string{"Set-Cookie", apiSessionCookie(nt, ns.MaxAge())})
	return nil, nil
}
