package auth

// Two-factor authentication (TOTP, Google Authenticator compatible), the 2FA step of a login, recovery codes,
// the auth settings and the security audit log. Go only; every table is new (additive), so the Python rollback
// image keeps working on the same auth.db and simply ignores them.
//
//	mfa(username, secret_enc, factor_id, enabled, enabled_at, last_step, last_used,
//	    pending_enc, pending_fid, pending_sid, pending_at)    an enrolment is bound to the session that started it
//	mfa_recovery(username, code_hash, created, used_at)      10 one-time codes, only SHA-256 stored
//	mfa_pending(token_hash, username, created, remember, next, id_token, source, attempts)
//	                                                          a login that passed the password, waiting for the code
//	oidc_extra(state_hash, remember, created)                 "Keep me signed in" across the Keycloak round trip
//	auth_settings(key, value)                                 mfa_required_for_login
//	mfa_reenrol(username, set_at, actor)                      an admin reset 2FA: the user must enrol before anything else
//	auth_audit(id, at, actor, action, target, detail, ip, sid, method, outcome, reason)
//	                                                          enrol, disable, reset, recovery, revoke, policy; never a secret

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/brand"
)

const MFA_SCHEMA = `
CREATE TABLE IF NOT EXISTS mfa (username TEXT PRIMARY KEY, secret_enc TEXT, factor_id TEXT, enabled INTEGER NOT NULL DEFAULT 0,
  enabled_at INTEGER, last_step INTEGER NOT NULL DEFAULT 0, last_used INTEGER, pending_enc TEXT, pending_fid TEXT, pending_sid TEXT,
  pending_at INTEGER, pending_base TEXT);
CREATE TABLE IF NOT EXISTS mfa_recovery (username TEXT NOT NULL, code_hash TEXT NOT NULL, created INTEGER NOT NULL,
  used_at INTEGER, PRIMARY KEY (username, code_hash));
CREATE TABLE IF NOT EXISTS mfa_pending (token_hash TEXT PRIMARY KEY, username TEXT NOT NULL, created INTEGER NOT NULL,
  remember INTEGER NOT NULL DEFAULT 0, next TEXT NOT NULL DEFAULT '/', id_token TEXT, source TEXT NOT NULL DEFAULT 'local',
  attempts INTEGER NOT NULL DEFAULT 0, auth_at INTEGER);
CREATE TABLE IF NOT EXISTS oidc_extra (state_hash TEXT PRIMARY KEY, remember INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS auth_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS mfa_reenrol (username TEXT PRIMARY KEY, set_at INTEGER NOT NULL, actor TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS auth_audit (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, actor TEXT NOT NULL,
  action TEXT NOT NULL, target TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', ip TEXT NOT NULL DEFAULT '',
  sid TEXT NOT NULL DEFAULT '', method TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL DEFAULT 'ok', reason TEXT NOT NULL DEFAULT '')
`

const (
	PENDING_TTL      = 300     // seconds to type the code after the password
	PENDING_TRIES    = 5       // wrong codes before the pending login is dropped
	ENROL_TTL        = 10 * 60 // an unconfirmed enrolment is forgotten after this
	FRESH_AUTH       = 10 * 60 // starting an enrolment needs a primary authentication this recent
	RECOVERY_CODES   = 10
	MFA_USER_MAXFAIL = 10 // wrong codes of one kind per user (any IP) before a 15-minute lock
	PENDING_PER_USER = 5  // concurrent pending logins per user (several browsers)
)

// Rate-limit budgets (failures table keys): each kind has its own, so failures of one kind never lock another
// path. Password: user|ip (login). TOTP and recovery: per user+IP and per user. Enrolment: per user.
func budget(kind, username, ip string) string {
	if ip == "" {
		return kind + "|" + username
	}
	return kind + "|" + username + "|" + ip
}

func parseOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "1", "true", "yes":
		return true
	}
	return false
}

// Policy (environment; MFA_REQUIRED_FOR_LOGIN can also be switched on from Users and access).
var (
	MFAActions   = envOr("MFA_REQUIRED_FOR_ACTIONS", "1") != "0"
	MFAStepup    = envInt("MFA_STEPUP_MINUTES", 720, 5, 60*24*31) * 60
	MFASensitive = envInt("MFA_SENSITIVE_MINUTES", 10, 1, 24*60) * 60
	// MFAStepUpOn: "on" asks for a fresh code by route class (MFA_STEPUP_MINUTES / MFA_SENSITIVE_MINUTES); "off"
	// (default) lets a session that passed 2FA once (at sign-in, at enrolment or via a trusted IdP) make every change
	// for its whole lifetime. Turning off your own 2FA and new recovery codes always ask for a code (inline).
	MFAStepUpOn   = parseOn(envOr("MFA_STEP_UP", "off"))
	MFALoginEnv   = envOr("MFA_REQUIRED_FOR_LOGIN", "0") == "1"
	MFATrustIDP   = envOr("MFA_TRUST_IDP", "0") == "1" // and OIDC_MFA_ACR / OIDC_MFA_AMR set explicitly
	MFAIssuerName = strings.TrimSpace(envOr("COMPANY_NAME", defIssuerCompany) + " " + brand.Product)
)

var (
	ErrBadCode   = errors.New("the code is wrong or was already used")
	ErrLocked    = errors.New("too many attempts")
	ErrNoEnrol   = errors.New("no enrolment in progress")
	ErrNotActive = errors.New("two-factor authentication is not on")
	ErrStale     = errors.New("confirm your password (or sign in again) first")
	ErrHasFactor = errors.New("two-factor authentication is already on")
)

// Event is one row of the security audit log (never a secret or a code).
type Event struct {
	Actor, Action, Target, Detail, IP string
	SID                               string // the actor's session id (token hash prefix)
	Method                            string // how the actor proved it: password, totp, recovery, idp, cli
	Outcome                           string // ok | denied | failed
	Reason                            string // the reason an admin gave
}

// Hook receives every event after it is stored (the app mirrors resets and recovery use to the notifications).
var Hook func(Event)

// Log stores an event.
func Log(e Event) {
	if e.Outcome == "" {
		e.Outcome = "ok"
	}
	Con().Exec("INSERT INTO auth_audit (at, actor, action, target, detail, ip, sid, method, outcome, reason) VALUES (?,?,?,?,?,?,?,?,?,?)",
		nowInt(), clip(e.Actor, 60), clip(e.Action, 40), clip(e.Target, 60), clip(e.Detail, 200), clip(e.IP, 64), clip(e.SID, 16),
		clip(e.Method, 20), clip(e.Outcome, 10), clip(e.Reason, 300))
	if Hook != nil {
		Hook(e)
	}
}

// Audit is Log with the common fields.
func Audit(actor, action, target, detail, ip string) {
	Log(Event{Actor: actor, Action: action, Target: target, Detail: detail, IP: ip})
}

// AuditRow is a row of auth_audit.
type AuditRow struct {
	At                                int64
	Actor, Action, Target, Detail, IP string
	Method, Outcome, Reason           string
}

// AuditLog is the newest n audit rows.
func AuditLog(n int) []AuditRow {
	rows, err := Con().Query("SELECT at, actor, action, target, detail, ip, method, outcome, reason FROM auth_audit ORDER BY id DESC LIMIT ?", n)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if rows.Scan(&r.At, &r.Actor, &r.Action, &r.Target, &r.Detail, &r.IP, &r.Method, &r.Outcome, &r.Reason) == nil {
			out = append(out, r)
		}
	}
	return out
}

// Setting / SetSetting: auth_settings.
func Setting(k string) string {
	var v string
	Con().QueryRow("SELECT value FROM auth_settings WHERE key=?", k).Scan(&v)
	return v
}

func SetSetting(k, v string) error {
	_, err := Con().Exec("INSERT INTO auth_settings (key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", k, v)
	return err
}

// MFALoginRequired: every user must have 2FA before using Argus (MFA_REQUIRED_FOR_LOGIN=1 or the admin switch).
func MFALoginRequired() bool { return MFALoginEnv || Setting("mfa_required_for_login") == "1" }

// --- status ----------------------------------------------------------------------------------------------

// MFAStatus is one user's 2FA state.
type MFAStatus struct {
	Enabled       bool
	EnabledAt     int64
	LastUsed      int64
	RecoveryLeft  int
	PendingSince  int64 // an enrolment started and not confirmed (0: none)
	NeedsReKeying bool
}

// Status is the 2FA state of a user.
func Status(username string) MFAStatus {
	var st MFAStatus
	var en int64
	var at, used, pat sql.NullInt64
	var box sql.NullString
	err := Con().QueryRow("SELECT enabled, enabled_at, last_used, pending_at, secret_enc FROM mfa WHERE username=?", username).
		Scan(&en, &at, &used, &pat, &box)
	if err != nil {
		return st
	}
	st.Enabled, st.EnabledAt, st.LastUsed = en != 0 && box.String != "", at.Int64, used.Int64
	if pat.Valid && pat.Int64 > nowInt()-ENROL_TTL {
		st.PendingSince = pat.Int64
	}
	st.NeedsReKeying = st.Enabled && NeedsRekey(box.String)
	Con().QueryRow("SELECT COUNT(*) FROM mfa_recovery WHERE username=? AND used_at IS NULL", username).Scan(&st.RecoveryLeft)
	return st
}

// Enabled says whether the user has a confirmed second factor.
func Enabled(username string) bool { return Status(username).Enabled }

// StatusAll is the 2FA state of every user (Users and access).
func StatusAll() map[string]MFAStatus {
	out := map[string]MFAStatus{}
	for _, u := range Users() {
		out[u.Username] = Status(u.Username)
	}
	return out
}

// --- enrolment -------------------------------------------------------------------------------------------
//
// An enrolment is a short transaction (ENROL_TTL) bound to the user and to the session that started it. Starting
// one needs a fresh primary authentication (FRESH_AUTH: the password typed again, or a Keycloak sign-in that
// recent); replacing an active factor also needs a current code or a recovery code. Nothing about the active
// factor changes until ConfirmEnrol succeeds, atomically, with a valid code for the new secret.

// BeginEnrol starts an enrolment for the session s and returns the new secret. currentCode is required (and
// checked) when the user already has 2FA.
func BeginEnrol(s *Session, currentCode, ip string) ([]byte, error) {
	u := s.Username
	if lockedN(budget("enrol", u, ""), MFA_USER_MAXFAIL) {
		return nil, ErrLocked
	}
	if s.AuthAt < nowInt()-FRESH_AUTH {
		return nil, ErrStale
	}
	base := "" // the factor this enrolment replaces ("" = none): bound into the transaction
	if Enabled(u) {
		if currentCode == "" {
			return nil, ErrHasFactor
		}
		_, fid, err := VerifyFactor(u, currentCode, ip)
		if err != nil {
			return nil, err
		}
		base = fid
	}
	secret, fid := randBytes(TOTP_BYTES), TokenHex(8)
	box, err := Seal(u, fid, secret)
	if err != nil {
		return nil, err
	}
	noteFailure(budget("enrol", u, "")) // counts starts too: at most MFA_USER_MAXFAIL per 15 min
	var res sql.Result
	if base == "" { // only while no factor is active (a factor activated meanwhile makes this a no-op)
		res, err = Con().Exec(`INSERT INTO mfa (username, pending_enc, pending_fid, pending_sid, pending_at, pending_base) VALUES (?,?,?,?,?,'')
		                     ON CONFLICT(username) DO UPDATE SET pending_enc=excluded.pending_enc, pending_fid=excluded.pending_fid,
		                     pending_sid=excluded.pending_sid, pending_at=excluded.pending_at, pending_base=''
		                     WHERE mfa.enabled=0`, u, box, fid, s.hash, nowInt())
	} else {
		res, err = Con().Exec(`UPDATE mfa SET pending_enc=?, pending_fid=?, pending_sid=?, pending_at=?, pending_base=?
		                     WHERE username=? AND enabled=1 AND COALESCE(factor_id,'')=?`, box, fid, s.hash, nowInt(), base, u, base)
	}
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrHasFactor
	}
	return secret, nil
}

// PendingSecret is the unconfirmed secret of the enrolment this session started (to show the QR code again).
func PendingSecret(s *Session) ([]byte, error) {
	sec, _, err := pendingSecret(s)
	return sec, err
}

func pendingSecret(s *Session) ([]byte, string, error) {
	var box, fid, sid sql.NullString
	var at sql.NullInt64
	if err := Con().QueryRow("SELECT pending_enc, pending_fid, pending_sid, pending_at FROM mfa WHERE username=?", s.Username).
		Scan(&box, &fid, &sid, &at); err != nil || box.String == "" || sid.String != s.hash || at.Int64 < nowInt()-ENROL_TTL {
		return nil, "", ErrNoEnrol
	}
	sec, err := Open(s.Username, fid.String, box.String)
	return sec, fid.String, err
}

// ConfirmEnrol activates the pending secret of this session when code is valid for it, replaces the recovery codes
// and returns the new ones (shown once). One atomic UPDATE: the transaction must still be this session's.
func ConfirmEnrol(s *Session, code, ip string) ([]string, error) {
	u := s.Username
	secret, pfid, err := pendingSecret(s)
	if err != nil {
		return nil, err
	}
	if !reserve("totp", u, ip) {
		return nil, ErrLocked
	}
	step := MatchStep(secret, code, nowInt(), 0)
	if step < 0 {
		return nil, ErrBadCode
	}
	codes := newRecoveryCodes()
	err = tx(func(t *sql.Tx) error {
		res, err := t.Exec(`UPDATE mfa SET secret_enc=pending_enc, factor_id=pending_fid, enabled=1, enabled_at=?, last_step=?, last_used=?,
		                    pending_enc=NULL, pending_fid=NULL, pending_sid=NULL, pending_at=NULL, pending_base=NULL
		                    WHERE username=? AND pending_enc IS NOT NULL AND pending_sid=? AND pending_fid=? AND pending_at >= ?
		                    AND COALESCE(factor_id,'') = COALESCE(pending_base,'') AND (enabled=0 OR COALESCE(pending_base,'') != '')`,
			nowInt(), step, nowInt(), u, s.hash, pfid, nowInt()-ENROL_TTL)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNoEnrol
		}
		if err := putRecovery(t, u, codes); err != nil {
			return err
		}
		_, err = t.Exec("DELETE FROM mfa_reenrol WHERE username=?", u) // a reset's forced enrolment is done
		return err
	})
	if err != nil {
		return nil, err
	}
	clearMFAFailures("totp", u, ip)
	Con().Exec("DELETE FROM failures WHERE key=?", budget("enrol", u, ""))
	Log(Event{Actor: u, Action: "mfa.enrol", Target: u, Detail: "authenticator app confirmed", IP: ip, SID: s.ID(), Method: "totp"})
	return codes, nil
}

// --- verification ----------------------------------------------------------------------------------------

func lockedMFA(kind, username, ip string) bool {
	return lockedN(budget(kind, username, ip), MAX_FAIL) || lockedN(budget(kind, username, ""), MFA_USER_MAXFAIL)
}

func lockedN(key string, max int64) bool {
	var count int64
	var until float64
	err := Con().QueryRow("SELECT count, until FROM failures WHERE key=?", key).Scan(&count, &until)
	return err == nil && count >= max && until > Now()
}

// reserve charges one attempt to the user+IP and per-user budgets of kind before the code is checked, in one
// write transaction (SQLite serialises them), so parallel requests cannot exceed the limits. false: locked.
// A success clears both (clearMFAFailures); a failure keeps the charge.
func reserve(kind, username, ip string) bool {
	ok := false
	now := nowInt()
	_ = tx(func(t *sql.Tx) error {
		for _, k := range [][2]any{{budget(kind, username, ip), int64(MAX_FAIL)}, {budget(kind, username, ""), int64(MFA_USER_MAXFAIL)}} {
			var count int64
			var until float64
			if err := t.QueryRow("SELECT count, until FROM failures WHERE key=?", k[0]).Scan(&count, &until); err == nil &&
				count >= k[1].(int64) && until > float64(now) {
				return nil
			}
		}
		for _, k := range []string{budget(kind, username, ip), budget(kind, username, "")} {
			var count int64
			var until float64
			n := int64(1)
			if err := t.QueryRow("SELECT count, until FROM failures WHERE key=?", k).Scan(&count, &until); err == nil && until > float64(now) {
				n = count + 1
			}
			if _, err := t.Exec("INSERT OR REPLACE INTO failures VALUES (?,?,?)", k, n, now+LOCK); err != nil {
				return err
			}
		}
		ok = true
		return nil
	})
	return ok
}

func failMFA(kind, username, ip string) {} // the attempt was charged by reserve

func clearMFAFailures(kind, username, ip string) {
	Con().Exec("DELETE FROM failures WHERE key IN (?, ?)", budget(kind, username, ip), budget(kind, username, ""))
}

// Verify checks a code from the authenticator app (or a recovery code) for an enrolled user. Only reached after
// the password (or with a session), so unauthenticated guesses never spend these budgets. Each attempt is charged
// before the check (reserve). A TOTP step is accepted once per factor: one UPDATE ... WHERE last_step < step AND
// factor_id = the one checked. A recovery code once. Returns "totp" or "recovery".
func Verify(username, code, ip string) (string, error) {
	via, _, err := VerifyFactor(username, code, ip)
	return via, err
}

// VerifyFactor is Verify that also returns the factor id the code proved, for a change bound to that factor.
func VerifyFactor(username, code, ip string) (string, string, error) {
	var box, fid string
	var last int64
	if err := Con().QueryRow("SELECT secret_enc, COALESCE(factor_id,''), last_step FROM mfa WHERE username=? AND enabled=1 AND secret_enc IS NOT NULL",
		username).Scan(&box, &fid, &last); err != nil {
		return "", "", ErrNotActive
	}
	if normCode(code) == "" && looksRecovery(code) {
		if !reserve("recovery", username, ip) {
			return "", "", ErrLocked
		}
		if useRecovery(username, code) {
			clearMFAFailures("recovery", username, ip)
			Con().Exec("UPDATE mfa SET last_used=? WHERE username=?", nowInt(), username)
			Log(Event{Actor: username, Action: "mfa.recovery.used", Target: username, Detail: "a recovery code was used",
				IP: ip, Method: "recovery"})
			return "recovery", fid, nil
		}
		return "", "", ErrBadCode
	}
	if !reserve("totp", username, ip) {
		return "", "", ErrLocked
	}
	secret, err := Open(username, fid, box)
	if err != nil {
		return "", "", err
	}
	step := MatchStep(secret, code, nowInt(), last)
	if step < 0 {
		return "", "", ErrBadCode
	}
	res, err := Con().Exec("UPDATE mfa SET last_step=?, last_used=? WHERE username=? AND last_step < ? AND COALESCE(factor_id,'')=?",
		step, nowInt(), username, step, fid)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 { // another request used this step first, or the factor changed
		return "", "", ErrBadCode
	}
	clearMFAFailures("totp", username, ip)
	return "totp", fid, nil
}

// Disable turns 2FA off for the user after a valid code (or recovery code) on a fresh primary authentication.
func Disable(s *Session, code, ip string) error {
	if s.AuthAt < nowInt()-FRESH_AUTH {
		return ErrStale
	}
	via, fid, err := VerifyFactor(s.Username, code, ip)
	if err != nil {
		return err
	}
	if err := wipeFactor(s.Username, fid); err != nil {
		return err
	}
	Log(Event{Actor: s.Username, Action: "mfa.disable", Target: s.Username, Detail: "turned off by the user", IP: ip, SID: s.ID(), Method: via})
	return nil
}

// AdminReset removes a user's 2FA (factor, pending enrolment, recovery codes), ends all their sessions and
// pending logins, and marks the account for re-enrolment: at their next sign-in (password or single sign-on) the
// user reaches only the 2FA setup until it is confirmed. The admin never sets it up for them. The caller checked
// the admin's fresh 2FA; reason is required. actor "cli:root" is `argus-api user reset-2fa`.
func AdminReset(actor, username, reason, ip, sid, method string) (int64, error) {
	if len([]rune(strings.TrimSpace(reason))) < 10 {
		return 0, errors.New("a reason of at least 10 characters is required")
	}
	if err := wipeAnd(username, func(t *sql.Tx) error {
		_, err := t.Exec("INSERT INTO mfa_reenrol (username, set_at, actor) VALUES (?,?,?) ON CONFLICT(username) DO UPDATE SET set_at=excluded.set_at, actor=excluded.actor",
			username, nowInt(), clip(actor, 60))
		return err
	}); err != nil {
		return 0, err
	}
	n := RevokeAll(username)
	Con().Exec("DELETE FROM failures WHERE key LIKE ? OR key LIKE ? OR key LIKE ?", "totp|"+username+"|%", "recovery|"+username+"|%",
		"enrol|"+username)
	Log(Event{Actor: actor, Action: "mfa.reset", Target: username, Detail: "2FA removed, re-enrolment required, " + strconv.FormatInt(n, 10) + " sessions ended",
		IP: ip, SID: sid, Method: method, Reason: strings.TrimSpace(reason)})
	return n, nil
}

// wipeFactor removes the factor only if it is still the one the code proved (never a replacement made meanwhile).
func wipeFactor(username, fid string) error {
	return tx(func(t *sql.Tx) error {
		res, err := t.Exec("DELETE FROM mfa WHERE username=? AND COALESCE(factor_id,'')=?", username, fid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrBadCode
		}
		_, err = t.Exec("DELETE FROM mfa_recovery WHERE username=?", username)
		return err
	})
}

func wipe(username string) error { return wipeAnd(username, nil) }

// wipeAnd removes the factor and recovery codes and runs also (when set) in the same transaction.
func wipeAnd(username string, also func(*sql.Tx) error) error {
	return tx(func(t *sql.Tx) error {
		if _, err := t.Exec("DELETE FROM mfa WHERE username=?", username); err != nil {
			return err
		}
		if _, err := t.Exec("DELETE FROM mfa_recovery WHERE username=?", username); err != nil {
			return err
		}
		if also != nil {
			return also(t)
		}
		return nil
	})
}

// ReenrolDue: an admin reset this user's 2FA and they have not set up a new one yet.
func ReenrolDue(username string) bool {
	var one int
	return Con().QueryRow("SELECT 1 FROM mfa_reenrol WHERE username=?", username).Scan(&one) == nil && !Enabled(username)
}

// --- recovery codes --------------------------------------------------------------------------------------
//
// A recovery code replaces the second factor once, never the password: it is only accepted after the password (or
// on a signed-in session). 16 characters of a 32-letter alphabet (80 bits), only a SHA-256 stored, consumed by one
// UPDATE, a new set replaces the whole old set.

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz023456789" // 32 letters: no o/1/l/i (0 is unambiguous without o)

func newRecoveryCodes() []string {
	out := make([]string, RECOVERY_CODES)
	for i := range out {
		b := randBytes(16)
		var s strings.Builder
		for j, x := range b {
			if j > 0 && j%4 == 0 {
				s.WriteByte('-')
			}
			s.WriteByte(recoveryAlphabet[int(x)%32]) // 256 % 32 == 0: no bias
		}
		out[i] = s.String()
	}
	return out
}

func normRecovery(code string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(code) {
		if strings.ContainsRune(recoveryAlphabet, r) {
			b.WriteRune(r)
		} else if r != '-' && r != ' ' {
			return ""
		}
	}
	return b.String()
}

func looksRecovery(code string) bool { return len(normRecovery(code)) == 16 }

func recoveryHash(username, code string) string {
	s := sha256.Sum256([]byte("argus-recovery|" + username + "|" + normRecovery(code)))
	return hex.EncodeToString(s[:])
}

func putRecovery(t *sql.Tx, username string, codes []string) error {
	if _, err := t.Exec("DELETE FROM mfa_recovery WHERE username=?", username); err != nil {
		return err
	}
	for _, c := range codes {
		if _, err := t.Exec("INSERT INTO mfa_recovery (username, code_hash, created) VALUES (?,?,?)", username, recoveryHash(username, c), nowInt()); err != nil {
			return err
		}
	}
	return nil
}

func useRecovery(username, code string) bool {
	res, err := Con().Exec("UPDATE mfa_recovery SET used_at=? WHERE username=? AND code_hash=? AND used_at IS NULL",
		nowInt(), username, recoveryHash(username, code))
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// NewRecovery replaces the whole set of recovery codes after a valid code and returns the new ones.
func NewRecovery(s *Session, code, ip string) ([]string, error) {
	via, fid, err := VerifyFactor(s.Username, code, ip)
	if err != nil {
		return nil, err
	}
	codes := newRecoveryCodes()
	if err := tx(func(t *sql.Tx) error {
		var one int
		if err := t.QueryRow("SELECT 1 FROM mfa WHERE username=? AND enabled=1 AND COALESCE(factor_id,'')=?", s.Username, fid).Scan(&one); err != nil {
			return ErrBadCode // the factor changed since the code was checked
		}
		return putRecovery(t, s.Username, codes)
	}); err != nil {
		return nil, err
	}
	Log(Event{Actor: s.Username, Action: "mfa.recovery.new", Target: s.Username, Detail: "recovery codes replaced", IP: ip, SID: s.ID(), Method: via})
	return codes, nil
}

// --- the 2FA step of a login ----------------------------------------------------------------------------

// Pending is a login that passed the password (or Keycloak) and waits for the code.
type Pending struct {
	Username, Next, Source string
	Remember               bool
	IDToken                sql.NullString
	Created, AuthAt        int64
}

// NewPending stores a pending login and returns its token (the __Host-opsr_mfa cookie).
func NewPending(username, next, source string, remember bool, idToken string) (string, error) {
	return NewPendingAt(username, next, source, remember, idToken, 0)
}

// NewPendingAt is NewPending with the primary authentication time (Keycloak's auth_time), carried unchanged into the
// session. Several browsers may each have one (PENDING_PER_USER); a new one never cancels another browser's.
func NewPendingAt(username, next, source string, remember bool, idToken string, authAt int64) (string, error) {
	token, now := TokenURLSafe(32), nowInt()
	var idt any
	if idToken != "" {
		idt = idToken
	}
	rem := 0
	if remember {
		rem = 1
	}
	err := tx(func(t *sql.Tx) error {
		if _, err := t.Exec("DELETE FROM mfa_pending WHERE created < ?", now-PENDING_TTL); err != nil {
			return err
		}
		if _, err := t.Exec(`DELETE FROM mfa_pending WHERE token_hash IN (SELECT token_hash FROM mfa_pending WHERE username=?
		                     ORDER BY created DESC LIMIT -1 OFFSET ?)`, username, PENDING_PER_USER-1); err != nil {
			return err
		}
		if authAt <= 0 || authAt > now {
			authAt = now
		}
		_, err := t.Exec("INSERT INTO mfa_pending (token_hash, username, created, remember, next, id_token, source, auth_at) VALUES (?,?,?,?,?,?,?,?)",
			tokenHash(token), username, now, rem, next, idt, source, authAt)
		return err
	})
	return token, err
}

// GetPending is the pending login of token, or nil (expired, unknown, too many wrong codes).
func GetPending(token string) *Pending {
	if token == "" {
		return nil
	}
	var p Pending
	var rem, tries int64
	var at sql.NullInt64
	err := Con().QueryRow(`SELECT username, next, source, remember, id_token, created, attempts, auth_at FROM mfa_pending WHERE token_hash=?`,
		tokenHash(token)).Scan(&p.Username, &p.Next, &p.Source, &rem, &p.IDToken, &p.Created, &tries, &at)
	p.AuthAt = at.Int64
	if err != nil || p.Created < nowInt()-PENDING_TTL || tries > PENDING_TRIES {
		return nil
	}
	p.Remember = rem != 0
	return &p
}

// PendingTry charges one attempt to a pending login before its code is checked (atomic); false when used up.
func PendingTry(token string) bool {
	res, err := Con().Exec("UPDATE mfa_pending SET attempts=attempts+1 WHERE token_hash=? AND attempts < ?", tokenHash(token), PENDING_TRIES)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// PendingFailed is kept for callers that count after the check; PendingTry is the atomic form.
func PendingFailed(token string) { PendingTry(token) }

// FinishPending turns a pending login into a full session with the second factor recorded (single use).
func FinishPending(token, via, ip, ua string) (string, *Pending, error) {
	p := GetPending(token)
	if p == nil {
		return "", nil, ErrNoEnrol
	}
	var st string
	err := tx(func(t *sql.Tx) error {
		res, err := t.Exec("DELETE FROM mfa_pending WHERE token_hash=?", tokenHash(token))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNoEnrol
		}
		o := Opts{Remember: p.Remember, MFAAt: nowInt(), MFAVia: via, IP: ip, UA: ua, AuthAt: p.AuthAt} // the code is not a primary auth
		if p.IDToken.Valid {
			o.IDToken = p.IDToken.String
		}
		st, err = newSession(t, p.Username, o)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return st, p, nil
}

// DropPending forgets a pending login.
func DropPending(token string) {
	if token != "" {
		Con().Exec("DELETE FROM mfa_pending WHERE token_hash=?", tokenHash(token))
	}
}

// --- Keycloak round trip --------------------------------------------------------------------------------

// OIDCPutRemember stores "Keep me signed in" for a pending Keycloak login (oidc_states stays the Python schema).
func OIDCPutRemember(stateHash string, remember bool) {
	now := nowInt()
	Con().Exec("DELETE FROM oidc_extra WHERE created < ?", now-STATE_TTL)
	if remember {
		Con().Exec("INSERT OR REPLACE INTO oidc_extra (state_hash, remember, created) VALUES (?,1,?)", stateHash, now)
	}
}

// OIDCTakeRemember returns and deletes the flag.
func OIDCTakeRemember(stateHash string) bool {
	res, err := Con().Exec("DELETE FROM oidc_extra WHERE state_hash=? AND remember=1 AND created >= ?", stateHash, nowInt()-STATE_TTL)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// --- key rotation ---------------------------------------------------------------------------------------

// Rekey re-encrypts every stored secret with the current key (TOTP_KEY_FILE), reading the old ones with
// TOTP_KEY_FILE_OLD. Returns how many secrets were rewritten.
func Rekey() (int, error) {
	if !KeyReady() {
		return 0, ErrNoKey
	}
	rows, err := Con().Query("SELECT username, COALESCE(secret_enc,''), COALESCE(factor_id,''), COALESCE(pending_enc,''), COALESCE(pending_fid,'') FROM mfa")
	if err != nil {
		return 0, err
	}
	type row struct{ u, s, f, p, pf string }
	var all []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.u, &r.s, &r.f, &r.p, &r.pf) == nil {
			all = append(all, r)
		}
	}
	rows.Close()
	n := 0
	for _, r := range all {
		for _, c := range [][3]string{{"secret_enc", r.s, r.f}, {"pending_enc", r.p, r.pf}} {
			col, box, fid := c[0], c[1], c[2]
			if box == "" || !NeedsRekey(box) {
				continue
			}
			plain, err := Open(r.u, fid, box)
			if err != nil {
				return n, errors.New("cannot decrypt the secret of " + r.u + ": set TOTP_KEY_FILE_OLD to the previous key")
			}
			nb, err := Seal(r.u, fid, plain)
			if err != nil {
				return n, err
			}
			if _, err := Con().Exec("UPDATE mfa SET "+col+"=? WHERE username=?", nb, r.u); err != nil {
				return n, err
			}
			n++
		}
	}
	if n > 0 {
		Log(Event{Actor: "cli:root", Action: "mfa.rekey", Detail: "secrets re-encrypted: " + strconv.Itoa(n), Method: "cli"})
	}
	return n, nil
}
