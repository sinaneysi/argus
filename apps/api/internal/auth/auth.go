// Package auth is app/auth.py: login, sessions and user management on auth.db (the app's only writable
// database). Same schema, same hashes, same rules:
//
//	users(username, hash, role viewer|admin, disabled, must_change, created_at)
//	user_meta(username, source local|keycloak, sub, last_login)
//	sessions(token_hash, username, created, last_seen, csrf, id_token, remember, mfa_at, mfa_via, ip, ua)
//	failures(key, count, until)
//	oidc_states(state_hash, nonce, verifier, next, created)
//
// Go only (additive: the Python rollback image ignores them): the session columns after id_token, and the tables
// of mfa.go (mfa, mfa_recovery, mfa_pending, oidc_extra, auth_settings, auth_audit).
//
// Passwords: scrypt (n=2^14, r=8, p=1, 32 bytes). Users imported from the old nginx htpasswd keep their
// sha512-crypt hash until their first login, when it is replaced by scrypt. Sessions: a random 32-byte token
// in the cookie, only its SHA-256 stored; lifetime 12 h, idle 2 h (SESSION_MAX_HOURS, SESSION_IDLE_HOURS), or with
// "Keep me signed in" 30 days, idle 7 days (SESSION_REMEMBER_DAYS, SESSION_REMEMBER_IDLE_DAYS). A new token at every
// login and at every 2FA step-up. 5 failures per user+IP (password or code) lock that pair 15 min.
package auth

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"golang.org/x/crypto/scrypt"

	_ "modernc.org/sqlite"
)

const (
	COOKIE     = "opsr_session"
	LIFETIME   = 12 * 3600 // defaults of SESSION_MAX_HOURS / SESSION_IDLE_HOURS
	IDLE       = 2 * 3600
	MAX_FAIL   = 5
	LOCK       = 15 * 60
	MIN_PW     = 12
	STATE_TTL  = 600
	MAX_STATES = 500
)

// DB is the auth.db path (ARGUS_AUTH_DB, old name OPS_AUTH_DB).
var DB = envOr("ARGUS_AUTH_DB", "/auth/auth.db")

// Session lifetimes in seconds (absolute from the login, and idle), plain and "Keep me signed in".
var (
	SessionMax   = envInt("SESSION_MAX_HOURS", 12, 1, 24*31) * 3600
	SessionIdle  = envInt("SESSION_IDLE_HOURS", 2, 1, 24*31) * 3600
	RememberMax  = min64(envInt("SESSION_REMEMBER_DAYS", 30, 1, 365), envInt("SESSION_REMEMBER_ABSOLUTE_DAYS", 30, 1, 365)) * 86400
	RememberIdle = envInt("SESSION_REMEMBER_IDLE_DAYS", 7, 1, 365) * 86400
)

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func envInt(k string, def, lo, hi int64) int64 {
	raw, _ := config.Get(k)
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || v < lo || v > hi {
		return def
	}
	return v
}

// Limits returns (absolute, idle) lifetime of a session kind.
func Limits(remember bool) (int64, int64) {
	if remember {
		return RememberMax, RememberIdle
	}
	return SessionMax, SessionIdle
}

const SCHEMA = `
CREATE TABLE IF NOT EXISTS users (username TEXT PRIMARY KEY, hash TEXT NOT NULL, role TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0, must_change INTEGER NOT NULL DEFAULT 0, created_at INTEGER);
CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, username TEXT NOT NULL,
  created INTEGER NOT NULL, last_seen INTEGER NOT NULL, csrf TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS failures (key TEXT PRIMARY KEY, count INTEGER NOT NULL, until INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_meta (username TEXT PRIMARY KEY, source TEXT NOT NULL DEFAULT 'local', sub TEXT,
  last_login INTEGER);
CREATE TABLE IF NOT EXISTS oidc_states (state_hash TEXT PRIMARY KEY, nonce TEXT NOT NULL, verifier TEXT NOT NULL,
  next TEXT NOT NULL, created INTEGER NOT NULL);
`

// envOr is a setting from the configuration loader (internal/config); d when the key is not set and has no
// schema default. k is the canonical name.
func envOr(k, d string) string {
	if v, ok := config.Get(k); ok {
		return v
	}
	return d
}

// Now is time.time() (tests may pin it).
var Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func nowInt() int64 { return int64(math.Floor(Now())) }

var (
	dbMu   sync.Mutex
	dbPath string
	dbConn *sql.DB
)

// Con returns the auth.db handle (one connection: SQLite serialises writers anyway, and the busy timeout
// matches Python's timeout=5).
func Con() *sql.DB {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbConn != nil && dbPath == DB {
		return dbConn
	}
	if dbConn != nil {
		dbConn.Close()
	}
	c, err := sql.Open("sqlite", "file:"+DB+"?_pragma=busy_timeout(5000)&_pragma=temp_store(2)&_txlock=immediate")
	if err != nil {
		panic(err)
	}
	c.SetMaxOpenConns(1)
	dbConn, dbPath = c, DB
	return c
}

// Reset closes the handle (tests that switch DB).
func Reset() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbConn != nil {
		dbConn.Close()
	}
	dbConn, dbPath = nil, ""
}

// tx runs f in one transaction (Python's `with con() as c:` block: commit on success, rollback on error).
func tx(f func(t *sql.Tx) error) error {
	t, err := Con().Begin()
	if err != nil {
		return err
	}
	if err := f(t); err != nil {
		t.Rollback()
		return err
	}
	return t.Commit()
}

// Init is auth.init(import_htpasswd, admins).
func Init(importHtpasswd string, admins []string) error {
	return tx(func(t *sql.Tx) error {
		for _, stmt := range strings.Split(SCHEMA, ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := t.Exec(stmt); err != nil {
				return err
			}
		}
		if importHtpasswd != "" {
			if f, err := os.Open(importHtpasswd); err == nil {
				sc := bufio.NewScanner(f)
				for sc.Scan() {
					name, h, _ := strings.Cut(strings.TrimSpace(sc.Text()), ":")
					if name != "" && strings.HasPrefix(h, "$6$") {
						role := "viewer"
						for _, a := range admins {
							if a == name {
								role = "admin"
							}
						}
						if _, err := t.Exec("INSERT OR IGNORE INTO users VALUES (?,?,?,0,0,?)", name, h, role, nowInt()); err != nil {
							f.Close()
							return err
						}
					}
				}
				f.Close()
			}
		}
		rows, err := t.Query("PRAGMA table_info(sessions)")
		if err != nil {
			return err
		}
		has := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			has[name] = true
		}
		rows.Close()
		// additive columns only (the Python image inserts sessions by column name and never reads these)
		for _, col := range [][2]string{{"id_token", "TEXT"}, {"remember", "INTEGER NOT NULL DEFAULT 0"}, {"mfa_at", "INTEGER"},
			{"mfa_via", "TEXT"}, {"ip", "TEXT"}, {"ua", "TEXT"}, {"auth_at", "INTEGER"}} {
			if !has[col[0]] {
				if _, err := t.Exec("ALTER TABLE sessions ADD COLUMN " + col[0] + " " + col[1]); err != nil {
					return err
				}
			}
		}
		for _, stmt := range strings.Split(MFA_SCHEMA, ";") {
			if strings.TrimSpace(stmt) != "" {
				if _, err := t.Exec(stmt); err != nil {
					return err
				}
			}
		}
		now := Now()
		if _, err := t.Exec(`DELETE FROM sessions WHERE (remember=0 AND (created < ? OR last_seen < ?))
		                     OR (remember!=0 AND (created < ? OR last_seen < ?))`,
			now-float64(SessionMax), now-float64(SessionIdle), now-float64(RememberMax), now-float64(RememberIdle)); err != nil {
			return err
		}
		if _, err := t.Exec("DELETE FROM mfa_pending WHERE created < ?", now-PENDING_TTL); err != nil {
			return err
		}
		_, err = t.Exec("DELETE FROM oidc_states WHERE created < ?", now-STATE_TTL)
		return err
	})
}

// BootstrapAdmin is auth.bootstrap_admin(username, initial_password).
func BootstrapAdmin(username, initial string) bool {
	if username == "" || initial == "" {
		return false
	}
	if len([]rune(initial)) < MIN_PW {
		fmt.Printf("OPS_ADMIN_INITIAL_PASSWORD shorter than %d characters; admin not created\n", MIN_PW)
		return false
	}
	created := false
	err := tx(func(t *sql.Tx) error {
		var one int
		if err := t.QueryRow("SELECT 1 FROM users WHERE username=?", username).Scan(&one); err == nil {
			return nil
		}
		h, err := HashPW(initial)
		if err != nil {
			return err
		}
		_, err = t.Exec("INSERT INTO users VALUES (?,?,?,0,1,?)", username, h, "admin", nowInt())
		created = err == nil
		return err
	})
	if err != nil || !created {
		return false
	}
	fmt.Printf("created admin %s from OPS_ADMIN_USER (must change password at first login)\n", pyRepr(username))
	return true
}

// --- passwords -----------------------------------------------------------------------------------

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// TokenURLSafe is secrets.token_urlsafe(n).
func TokenURLSafe(n int) string { return base64.RawURLEncoding.EncodeToString(randBytes(n)) }

// TokenHex is secrets.token_hex(n).
func TokenHex(n int) string { return hex.EncodeToString(randBytes(n)) }

// HashPW is hash_pw(pw): scrypt$16384$8$1$<salt hex>$<key hex>.
func HashPW(pw string) (string, error) {
	salt := randBytes(16)
	dk, err := scrypt.Key([]byte(pw), salt, 1<<14, 8, 1, 32)
	if err != nil {
		return "", err
	}
	return "scrypt$16384$8$1$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(dk), nil
}

// CheckPW is check_pw(pw, stored).
func CheckPW(pw, stored string) bool {
	if strings.HasPrefix(stored, "scrypt$") {
		parts := strings.Split(stored, "$")
		if len(parts) != 6 {
			return false
		}
		n, e1 := strconv.Atoi(parts[1])
		r, e2 := strconv.Atoi(parts[2])
		p, e3 := strconv.Atoi(parts[3])
		salt, e4 := hex.DecodeString(parts[4])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			return false
		}
		// Python's hashlib.scrypt refuses more than 32 MB (128*n*r); the same bound keeps a crafted hash from
		// costing the server more.
		if n < 2 || n&(n-1) != 0 || r < 1 || p < 1 || int64(128)*int64(n)*int64(r) > 32*1024*1024+1024*1024 || p > 16 {
			return false
		}
		got, err := scrypt.Key([]byte(pw), salt, n, r, p, 32)
		if err != nil {
			return false
		}
		return hmac.Equal([]byte(hex.EncodeToString(got)), []byte(parts[5]))
	}
	if strings.HasPrefix(stored, "$6$") {
		got, ok := SHA512Crypt(pw, stored)
		return ok && hmac.Equal([]byte(got), []byte(stored))
	}
	return false
}

// --- lockout -------------------------------------------------------------------------------------

func locked(key string) bool {
	var count int64
	var until float64
	err := Con().QueryRow("SELECT count, until FROM failures WHERE key=?", key).Scan(&count, &until)
	return err == nil && count >= MAX_FAIL && until > Now()
}

func noteFailure(key string) {
	now := nowInt()
	_ = tx(func(t *sql.Tx) error {
		var count int64
		var until float64
		n := int64(1)
		if err := t.QueryRow("SELECT count, until FROM failures WHERE key=?", key).Scan(&count, &until); err == nil && until > float64(now) {
			n = count + 1
		}
		_, err := t.Exec("INSERT OR REPLACE INTO failures VALUES (?,?,?)", key, n, now+LOCK)
		return err
	})
}

func tokenHash(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}

// Opts are the details of a new session.
type Opts struct {
	IDToken  any    // Keycloak id_token (nil for a local login)
	Remember bool   // "Keep me signed in"
	MFAAt    int64  // second factor verified at (0: not verified on this session)
	MFAVia   string // totp | recovery | idp
	AuthAt   int64  // primary authentication (password, or Keycloak's auth_time); 0 = now
	IP, UA   string
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func newSession(t *sql.Tx, username string, o Opts) (string, error) {
	return newSessionAt(t, username, o, nowInt())
}

func newSessionAt(t *sql.Tx, username string, o Opts, created int64) (string, error) {
	token, now := TokenURLSafe(32), nowInt()
	var mfaAt, mfaVia any
	if o.MFAAt > 0 {
		mfaAt, mfaVia = o.MFAAt, o.MFAVia
	}
	rem := 0
	if o.Remember {
		rem = 1
	}
	authAt := o.AuthAt
	if authAt <= 0 || authAt > now {
		authAt = now
	}
	if _, err := t.Exec(`INSERT INTO sessions (token_hash, username, created, last_seen, csrf, id_token, remember, mfa_at, mfa_via, ip, ua, auth_at)
	                     VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		tokenHash(token), username, created, now, TokenHex(16), o.IDToken, rem, mfaAt, mfaVia, clip(o.IP, 64), clip(o.UA, 200), authAt); err != nil {
		return "", err
	}
	if _, err := t.Exec(`INSERT INTO user_meta (username, source, last_login) VALUES (?, 'local', ?)
                 ON CONFLICT(username) DO UPDATE SET last_login=excluded.last_login`, username, now); err != nil {
		return "", err
	}
	return token, nil
}

// NewSession opens a session (after the password, or the password and the code).
func NewSession(username string, o Opts) (string, error) {
	var token string
	err := tx(func(t *sql.Tx) error {
		var err error
		token, err = newSession(t, username, o)
		return err
	})
	return token, err
}

// Source is source(username): "local" when there is no user_meta row.
func Source(username string) string {
	var s string
	if err := Con().QueryRow("SELECT source FROM user_meta WHERE username=?", username).Scan(&s); err != nil {
		return "local"
	}
	return s
}

// CheckPassword checks a local password with the lockout (5 failures per user+IP lock that pair 15 min), clears
// the failures and upgrades an old hash on success. It never says which part was wrong.
func CheckPassword(username, pw, ip string) bool {
	key := username + "|" + ip
	if locked(key) {
		return false
	}
	var hash string
	err := Con().QueryRow("SELECT hash FROM users WHERE username=? AND disabled=0", username).Scan(&hash)
	if err != nil || Source(username) != "local" || !CheckPW(pw, hash) {
		noteFailure(key)
		return false
	}
	err = tx(func(t *sql.Tx) error {
		if _, err := t.Exec("DELETE FROM failures WHERE key=?", key); err != nil {
			return err
		}
		if !strings.HasPrefix(hash, "scrypt$") {
			h, err := HashPW(pw)
			if err != nil {
				return err
			}
			if _, err := t.Exec("UPDATE users SET hash=? WHERE username=?", h, username); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("login: %v", err)
		return false
	}
	return true
}

// Login is login(username, pw, ip): a new session token, or "" (never says which part was wrong). The server
// uses CheckPassword and then NewSession or a 2FA pending login; this stays for the tools and tests.
func Login(username, pw, ip string) string {
	if !CheckPassword(username, pw, ip) {
		return ""
	}
	token, err := NewSession(username, Opts{IP: ip})
	if err != nil {
		log.Printf("login: %v", err)
		return ""
	}
	return token
}

// SSOLogin is sso_login(username, sub, role, id_token, reserved): (token, "") or ("", reason).
func SSOLogin(username, sub, role, idToken string, reserved []string) (string, string) {
	return SSOLoginWith(username, sub, role, reserved, Opts{IDToken: idToken}, false)
}

// SSOLoginWith provisions the Keycloak user and opens a session with these options, or (provisionOnly) only
// provisions it and returns ("", "") on success: the session then waits for the Argus code (mfa_pending).
func SSOLoginWith(username, sub, role string, reserved []string, o Opts, provisionOnly bool) (string, string) {
	now := nowInt()
	var token, why string
	err := tx(func(t *sql.Tx) error {
		var disabled int64
		uerr := t.QueryRow("SELECT disabled FROM users WHERE username=?", username).Scan(&disabled)
		haveU := uerr == nil
		var msource string
		var msub sql.NullString
		merr := t.QueryRow("SELECT source, sub FROM user_meta WHERE username=?", username).Scan(&msource, &msub)
		sso := merr == nil && msource == "keycloak"
		if sso && msub.Valid && msub.String != "" && msub.String != sub {
			why = "the Argus account belongs to another Keycloak user"
			return nil
		}
		if haveU && disabled != 0 {
			why = "the account is disabled in Argus"
			return nil
		}
		isReserved := false
		for _, r := range reserved {
			if r == username {
				isReserved = true
			}
		}
		if haveU && !sso && isReserved {
			why = "the name is a reserved local account"
			return nil
		}
		if haveU {
			if _, err := t.Exec("UPDATE users SET hash='', role=?, must_change=0 WHERE username=?", role, username); err != nil {
				return err
			}
		} else if _, err := t.Exec("INSERT INTO users (username, hash, role, disabled, must_change, created_at) VALUES (?, '', ?, 0, 0, ?)",
			username, role, now); err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT INTO user_meta (username, source, sub, last_login) VALUES (?, 'keycloak', ?, ?)
                     ON CONFLICT(username) DO UPDATE SET source='keycloak', sub=excluded.sub`, username, sub, now); err != nil {
			return err
		}
		if provisionOnly {
			return nil
		}
		var err error
		token, err = newSession(t, username, o)
		return err
	})
	if err != nil {
		return "", "the sign-in could not be saved"
	}
	if why != "" {
		return "", why
	}
	return token, ""
}

// OIDCState is a pending Keycloak login.
type OIDCState struct {
	Nonce, Verifier, Next string
	Created               int64
}

// OIDCPut is oidc_put(state_hash, nonce, verifier, next).
func OIDCPut(stateHash, nonce, verifier, next string) error {
	now := nowInt()
	return tx(func(t *sql.Tx) error {
		if _, err := t.Exec("DELETE FROM oidc_states WHERE created < ?", now-STATE_TTL); err != nil {
			return err
		}
		if _, err := t.Exec(`DELETE FROM oidc_states WHERE state_hash IN (SELECT state_hash FROM oidc_states
                     ORDER BY created DESC LIMIT -1 OFFSET ?)`, MAX_STATES-1); err != nil {
			return err
		}
		_, err := t.Exec("INSERT INTO oidc_states VALUES (?,?,?,?,?)", stateHash, nonce, verifier, next, now)
		return err
	})
}

// OIDCTake is oidc_take(state_hash): return and delete a pending login (single use), or nil.
func OIDCTake(stateHash string) *OIDCState {
	var st *OIDCState
	_ = tx(func(t *sql.Tx) error {
		var s OIDCState
		if err := t.QueryRow("SELECT nonce, verifier, next, created FROM oidc_states WHERE state_hash=?", stateHash).
			Scan(&s.Nonce, &s.Verifier, &s.Next, &s.Created); err != nil {
			return nil
		}
		res, err := t.Exec("DELETE FROM oidc_states WHERE state_hash=?", stateHash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil
		}
		st = &s
		return nil
	})
	return st
}

// Session is what session() returns for a valid session.
type Session struct {
	Username   string
	Role       string
	MustChange int64
	CSRF       string
	Source     string
	// Go only
	Remember bool
	Created  int64
	MFAAt    int64  // 0: no second factor on this session
	MFAVia   string // totp | recovery | idp
	AuthAt   int64  // primary authentication time
	hash     string
}

// SID is the session's token hash prefix for the audit log (never the token).
func (s *Session) SID() string { return s.ID() }

// ID is the short public id of a session (the first 16 hex of its token hash): enough to name it on the Account
// page, useless to sign in with.
func (s *Session) ID() string {
	if len(s.hash) < 16 {
		return ""
	}
	return s.hash[:16]
}

// GetSession is session(token): the session, or nil. Plain sessions last SessionMax from the login and
// SessionIdle without a request; "Keep me signed in" ones RememberMax and RememberIdle (sliding: every request
// moves the idle limit, never the absolute one).
func GetSession(token string) *Session {
	if token == "" {
		return nil
	}
	th, now := tokenHash(token), nowInt()
	var s Session
	var created, lastSeen, rem int64
	var mfaAt, authAt sql.NullInt64
	var mfaVia sql.NullString
	err := Con().QueryRow(`SELECT s.username, s.created, s.last_seen, s.csrf, u.role, u.must_change,
                         COALESCE(m.source, 'local') AS source, COALESCE(s.remember, 0), s.mfa_at, s.mfa_via, s.auth_at FROM sessions s
                         JOIN users u ON u.username=s.username LEFT JOIN user_meta m ON m.username=s.username
                         WHERE s.token_hash=? AND u.disabled=0`, th).
		Scan(&s.Username, &created, &lastSeen, &s.CSRF, &s.Role, &s.MustChange, &s.Source, &rem, &mfaAt, &mfaVia, &authAt)
	if err != nil {
		return nil
	}
	s.Remember, s.Created, s.MFAAt, s.MFAVia, s.hash = rem != 0, created, mfaAt.Int64, mfaVia.String, th
	s.AuthAt = authAt.Int64
	if s.AuthAt == 0 {
		s.AuthAt = created
	}
	max, idle := Limits(s.Remember)
	if created < now-max || lastSeen < now-idle {
		Con().Exec("DELETE FROM sessions WHERE token_hash=?", th)
		return nil
	}
	if now-lastSeen > 60 {
		Con().Exec("UPDATE sessions SET last_seen=? WHERE token_hash=?", now, th)
	}
	return &s
}

// TouchClient records the client address and browser of a session (shown on the Account page); cheap no-op when
// unchanged.
func TouchClient(token, ip, ua string) {
	if token == "" {
		return
	}
	Con().Exec("UPDATE sessions SET ip=?, ua=? WHERE token_hash=? AND (ip IS NOT ? OR ua IS NOT ?)",
		clip(ip, 64), clip(ua, 200), tokenHash(token), clip(ip, 64), clip(ua, 200))
}

// MaxAge is the cookie Max-Age for a session: what is left of its absolute lifetime.
func (s *Session) MaxAge() int64 {
	max, _ := Limits(s.Remember)
	left := s.Created + max - nowInt()
	if left < 0 {
		return 0
	}
	return left
}

// RotateFresh is Rotate after a new primary authentication on the same session (password re-entered).
func RotateFresh(token string) string {
	nt := Rotate(token, 0, "")
	if nt != "" {
		Con().Exec("UPDATE sessions SET auth_at=? WHERE token_hash=?", nowInt(), tokenHash(nt))
	}
	return nt
}

// Rotate replaces a session's token (and CSRF secret) by new ones, keeping its user, kind and start; mfaAt > 0
// records a second factor on it. The old token stops working at once. Returns the new token, or "".
func Rotate(token string, mfaAt int64, via string) string {
	if token == "" {
		return ""
	}
	th := tokenHash(token)
	var nt string
	err := tx(func(t *sql.Tx) error {
		var user string
		var created, rem int64
		var idt, ip, ua sql.NullString
		var oldAt, authAt sql.NullInt64
		var oldVia sql.NullString
		if err := t.QueryRow("SELECT username, created, COALESCE(remember,0), id_token, ip, ua, mfa_at, mfa_via, auth_at FROM sessions WHERE token_hash=?", th).
			Scan(&user, &created, &rem, &idt, &ip, &ua, &oldAt, &oldVia, &authAt); err != nil {
			return err
		}
		// a rotation never extends the 2FA grant or the primary authentication: only a new verification does
		o := Opts{Remember: rem != 0, IP: ip.String, UA: ua.String, MFAAt: oldAt.Int64, MFAVia: oldVia.String, AuthAt: authAt.Int64}
		if idt.Valid {
			o.IDToken = idt.String
		}
		if mfaAt > 0 {
			o.MFAAt, o.MFAVia = mfaAt, via
		}
		if _, err := t.Exec("DELETE FROM sessions WHERE token_hash=?", th); err != nil {
			return err
		}
		var err error
		nt, err = newSessionAt(t, user, o, created)
		return err
	})
	if err != nil {
		return ""
	}
	return nt
}

// SessionInfo is a row of the Account page's "Your sessions".
type SessionInfo struct {
	ID                string
	Created, LastSeen int64
	IP, UA            string
	Remember          bool
	MFAAt             int64
	MFAVia            string
	Current           bool
}

// UserSessions lists a user's live sessions, the newest first (current marks the one of token).
func UserSessions(username, current string) []SessionInfo {
	rows, err := Con().Query(`SELECT token_hash, created, last_seen, COALESCE(ip,''), COALESCE(ua,''), COALESCE(remember,0),
	                          COALESCE(mfa_at,0), COALESCE(mfa_via,'') FROM sessions WHERE username=? ORDER BY last_seen DESC, created DESC`, username)
	if err != nil {
		return nil
	}
	defer rows.Close()
	cur, now := tokenHash(current), nowInt()
	var out []SessionInfo
	for rows.Next() {
		var x SessionInfo
		var th string
		var rem int64
		if rows.Scan(&th, &x.Created, &x.LastSeen, &x.IP, &x.UA, &rem, &x.MFAAt, &x.MFAVia) != nil {
			continue
		}
		x.Remember, x.Current, x.ID = rem != 0, th == cur, th[:16]
		max, idle := Limits(x.Remember)
		if x.Created < now-max || x.LastSeen < now-idle {
			continue
		}
		out = append(out, x)
	}
	return out
}

// RevokeSession ends one of a user's sessions by its public id; true when one was ended.
func RevokeSession(username, id string) bool {
	if len(id) != 16 || strings.Trim(id, "0123456789abcdef") != "" {
		return false
	}
	res, err := Con().Exec("DELETE FROM sessions WHERE username=? AND substr(token_hash,1,16)=?", username, id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// RevokeOthers ends every session of the user except the one of token; the number ended.
func RevokeOthers(username, token string) int64 {
	res, err := Con().Exec("DELETE FROM sessions WHERE username=? AND token_hash != ?", username, tokenHash(token))
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// RevokeAll ends every session of the user (admin "Sign out everywhere", 2FA reset); the number ended.
func RevokeAll(username string) int64 {
	res, err := Con().Exec("DELETE FROM sessions WHERE username=?", username)
	if err != nil {
		return 0
	}
	Con().Exec("DELETE FROM mfa_pending WHERE username=?", username)
	n, _ := res.RowsAffected()
	return n
}

// Logout is logout(token): delete the session, return its id_token ("" when none).
func Logout(token string) string {
	if token == "" {
		return ""
	}
	th := tokenHash(token)
	var idt sql.NullString
	_ = tx(func(t *sql.Tx) error {
		_ = t.QueryRow("SELECT id_token FROM sessions WHERE token_hash=?", th).Scan(&idt)
		_, err := t.Exec("DELETE FROM sessions WHERE token_hash=?", th)
		return err
	})
	return idt.String
}

// ChangePassword is change_password(username, current, new): "" on success, else the message.
func ChangePassword(username, current, nw string) string {
	if Source(username) != "local" {
		return "This account signs in with Keycloak; its password is managed there."
	}
	var hash string
	if err := Con().QueryRow("SELECT hash FROM users WHERE username=?", username).Scan(&hash); err != nil || !CheckPW(current, hash) {
		return "Current password is wrong."
	}
	if len([]rune(nw)) < MIN_PW {
		return fmt.Sprintf("New password must be at least %d characters.", MIN_PW)
	}
	if nw == current {
		return "New password must differ from the current one."
	}
	h, err := HashPW(nw)
	if err != nil {
		return "Could not save the password."
	}
	if _, err := Con().Exec("UPDATE users SET hash=?, must_change=0 WHERE username=?", h, username); err != nil {
		panic(err) // Python raised sqlite3.Error here: a 500
	}
	return ""
}

// User is a row of users().
type User struct {
	Username   string
	Role       string
	Disabled   int64
	MustChange int64
	CreatedAt  sql.NullInt64
	Source     string
	LastLogin  sql.NullInt64
}

// Users is users(), ordered by name.
func Users() []User {
	rows, err := Con().Query(`SELECT u.username, u.role, u.disabled, u.must_change, u.created_at,
                            COALESCE(m.source, 'local') AS source, m.last_login FROM users u
                            LEFT JOIN user_meta m ON m.username=u.username ORDER BY u.username`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.Username, &u.Role, &u.Disabled, &u.MustChange, &u.CreatedAt, &u.Source, &u.LastLogin); err == nil {
			out = append(out, u)
		}
	}
	return out
}

// TempPassword is temp_password().
func TempPassword() string { return TokenURLSafe(12) }

// AddUser is add_user(username, role): the temporary password.
func AddUser(username, role string) (string, error) {
	pw := TempPassword()
	h, err := HashPW(pw)
	if err != nil {
		return "", err
	}
	err = tx(func(t *sql.Tx) error {
		if _, err := t.Exec("INSERT INTO users VALUES (?,?,?,0,1,?)", username, h, role, nowInt()); err != nil {
			return err
		}
		_, err := t.Exec("DELETE FROM user_meta WHERE username=?", username)
		return err
	})
	return pw, err
}

// ResetPassword is reset_password(username): the temporary password.
func ResetPassword(username string) (string, error) {
	pw := TempPassword()
	h, err := HashPW(pw)
	if err != nil {
		return "", err
	}
	err = tx(func(t *sql.Tx) error {
		if _, err := t.Exec("UPDATE users SET hash=?, must_change=1 WHERE username=?", h, username); err != nil {
			return err
		}
		_, err := t.Exec("DELETE FROM sessions WHERE username=?", username)
		return err
	})
	return pw, err
}

// SetRole is set_role(username, role).
func SetRole(username, role string) error {
	_, err := Con().Exec("UPDATE users SET role=? WHERE username=?", role, username)
	return err
}

// SetDisabled is set_disabled(username, disabled).
func SetDisabled(username string, disabled bool) error {
	d := 0
	if disabled {
		d = 1
	}
	return tx(func(t *sql.Tx) error {
		if _, err := t.Exec("UPDATE users SET disabled=? WHERE username=?", d, username); err != nil {
			return err
		}
		if disabled {
			_, err := t.Exec("DELETE FROM sessions WHERE username=?", username)
			return err
		}
		return nil
	})
}

// AdminCount is admin_count().
func AdminCount() int {
	var n int
	Con().QueryRow("SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0").Scan(&n)
	return n
}

// ErrNoRow is returned by helpers that find nothing.
var ErrNoRow = errors.New("no row")

func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}
