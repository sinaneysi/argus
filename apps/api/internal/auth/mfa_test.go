package auth

// Sessions (remember me, sliding idle, absolute limit, rotation, revoke) and TOTP 2FA (RFC 6238 vectors, the
// window, replay, lockout, recovery codes, encryption at rest, the pending login, key rotation).

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	rand.Read(k)
	p := filepath.Join(t.TempDir(), "totp.key")
	os.WriteFile(p, k, 0o400)
	oldK, oldO := KeyFile, OldKeyFile
	KeyFile, OldKeyFile = p, ""
	ResetKeys()
	t.Cleanup(func() { KeyFile, OldKeyFile = oldK, oldO; ResetKeys() })
	return k
}

func pinNow(t *testing.T, at float64) func(float64) {
	real := Now
	cur := at
	Now = func() float64 { return cur }
	t.Cleanup(func() { Now = real })
	return func(d float64) { cur += d }
}

func addUser(t *testing.T, name string) {
	t.Helper()
	h, _ := HashPW("Password-12345")
	if _, err := Con().Exec("INSERT INTO users VALUES (?,?,?,0,0,0)", name, h, "admin"); err != nil {
		t.Fatal(err)
	}
}

// RFC 6238 appendix B: the seeds are the ASCII "1234567890" repeated to the hash's size; 8 digits.
func TestTOTPRFC6238Vectors(t *testing.T) {
	seeds := map[string][]byte{"SHA1": []byte("12345678901234567890"),
		"SHA256": []byte("12345678901234567890123456789012"),
		"SHA512": []byte("1234567890123456789012345678901234567890123456789012345678901234")}
	vec := []struct {
		t    int64
		want map[string]string
	}{
		{59, map[string]string{"SHA1": "94287082", "SHA256": "46119246", "SHA512": "90693936"}},
		{1111111109, map[string]string{"SHA1": "07081804", "SHA256": "68084774", "SHA512": "25091201"}},
		{1111111111, map[string]string{"SHA1": "14050471", "SHA256": "67062674", "SHA512": "99943326"}},
		{1234567890, map[string]string{"SHA1": "89005924", "SHA256": "91819424", "SHA512": "93441116"}},
		{2000000000, map[string]string{"SHA1": "69279037", "SHA256": "90698825", "SHA512": "38618901"}},
		{20000000000, map[string]string{"SHA1": "65353130", "SHA256": "77737706", "SHA512": "47863826"}},
	}
	for _, v := range vec {
		for alg, want := range v.want {
			if got := HOTP(seeds[alg], uint64(Step(v.t)), 8, HashByName(alg)); got != want {
				t.Errorf("T=%d %s: %s, want %s", v.t, alg, got, want)
			}
		}
	}
	// the 6-digit Google Authenticator code is the last 6 digits of the 8-digit one
	if TOTPAt(seeds["SHA1"], Step(59)) != "287082" {
		t.Fatal("6 digits")
	}
	// RFC 4226 appendix D (HOTP, 6 digits)
	for i, want := range []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"} {
		if got := HOTP(seeds["SHA1"], uint64(i), 6, HashByName("SHA1")); got != want {
			t.Errorf("HOTP %d: %s", i, got)
		}
	}
	if s, _ := B32Dec("gezd gnbv-gy3t qojq"); string(s) != "1234567890" || B32([]byte("1234567890")) != "GEZDGNBVGY3TQOJQ" {
		t.Fatal("base32")
	}
	u := OTPAuthURI("Northwind Argus", "alex.doe", []byte("12345678901234567890"))
	if u != "otpauth://totp/Argus:alex.doe?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=Northwind%20Argus&algorithm=SHA1&digits=6&period=30" {
		t.Fatal(u)
	}
}

func TestTOTPWindow(t *testing.T) {
	sec := []byte("12345678901234567890")
	now := int64(1_700_000_015)
	for d, ok := range map[int64]bool{-60: false, -30: true, 0: true, 30: true, 60: false} {
		code := TOTPAt(sec, Step(now+d))
		if got := MatchStep(sec, code, now, 0) >= 0; got != ok {
			t.Errorf("offset %d: %v", d, got)
		}
	}
	code := TOTPAt(sec, Step(now))
	if MatchStep(sec, code, now, Step(now)) >= 0 {
		t.Fatal("a step not newer than the last used one is refused")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "12 34 5x"} {
		if MatchStep(sec, bad, now, 0) >= 0 {
			t.Errorf("accepted %q", bad)
		}
	}
	if MatchStep(sec, code[:3]+" "+code[3:], now, 0) < 0 {
		t.Fatal("a space in the middle is fine")
	}
}

func sess(t *testing.T, user string) *Session {
	t.Helper()
	tok, err := NewSession(user, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	return GetSession(tok)
}

func enrol(t *testing.T, user string) ([]byte, []string) {
	t.Helper()
	s := sess(t, user)
	sec, err := BeginEnrol(s, "", "ip")
	if err != nil {
		t.Fatal(err)
	}
	if Enabled(user) {
		t.Fatal("enabled before the code was confirmed")
	}
	if _, err := ConfirmEnrol(s, "000000", "ip"); err == nil && TOTPAt(sec, Step(nowInt())) != "000000" {
		t.Fatal("wrong code confirmed the enrolment")
	}
	// another session of the same user cannot confirm this enrolment
	if _, err := ConfirmEnrol(sess(t, user), TOTPAt(sec, Step(nowInt())), "ip"); err != ErrNoEnrol {
		t.Fatal("enrolment not bound to its session", err)
	}
	codes, err := ConfirmEnrol(s, TOTPAt(sec, Step(nowInt())), "ip")
	if err != nil || len(codes) != RECOVERY_CODES {
		t.Fatal("confirm", err, len(codes))
	}
	return sec, codes
}

func TestMFAEnrolVerifyReplayLockout(t *testing.T) {
	setup(t)
	withKey(t)
	adv := pinNow(t, 1_800_000_000)
	addUser(t, "ann")
	if _, err := Verify("ann", "123456", "ip"); err != ErrNotActive {
		t.Fatal("not enrolled", err)
	}
	sec, _ := enrol(t, "ann")
	st := Status("ann")
	if !st.Enabled || st.RecoveryLeft != 10 || st.PendingSince != 0 {
		t.Fatalf("status %+v", st)
	}
	// the code used to confirm cannot be used again (same step)
	if _, err := Verify("ann", TOTPAt(sec, Step(nowInt())), "ip"); err != ErrBadCode {
		t.Fatal("replay of the enrolment code", err)
	}
	adv(30)
	code := TOTPAt(sec, Step(nowInt()))
	if via, err := Verify("ann", code, "ip"); err != nil || via != "totp" {
		t.Fatal("next step", err)
	}
	if _, err := Verify("ann", code, "ip2"); err != ErrBadCode {
		t.Fatal("replay from another IP", err)
	}
	// an older step (inside the window) after a newer one was used: refused
	adv(30)
	if _, err := Verify("ann", TOTPAt(sec, Step(nowInt())-2), "ip"); err != ErrBadCode {
		t.Fatal("old step")
	}
	// lockout: TOTP has its own budget per user+IP; it does not lock the password or the recovery codes
	for i := 0; i < MAX_FAIL; i++ {
		Verify("ann", "000001", "evil")
	}
	if _, err := Verify("ann", TOTPAt(sec, Step(nowInt())), "evil"); err != ErrLocked {
		t.Fatal("not locked", err)
	}
	if !CheckPassword("ann", "Password-12345", "evil") {
		t.Fatal("wrong codes must not lock the password path (separate budgets)")
	}
	if _, err := Verify("ann", TOTPAt(sec, Step(nowInt())), "ip"); err != nil {
		t.Fatal("another IP still works", err)
	}
	adv(LOCK + 1)
	adv(30)
	if _, err := Verify("ann", TOTPAt(sec, Step(nowInt())), "evil"); err != nil {
		t.Fatal("lock expired", err)
	}
	// per-user limit across IPs
	for i := 0; i < MFA_USER_MAXFAIL; i++ {
		Verify("ann", "000001", "ip-"+string(rune('a'+i)))
	}
	adv(30)
	if _, err := Verify("ann", TOTPAt(sec, Step(nowInt())), "fresh-ip"); err != ErrLocked {
		t.Fatal("per-user lock", err)
	}
}

func TestMFARecoveryCodesSingleUse(t *testing.T) {
	setup(t)
	withKey(t)
	pinNow(t, 1_800_000_000)
	addUser(t, "bob")
	_, codes := enrol(t, "bob")
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 19 || seen[c] {
			t.Fatal("code format or duplicate", c)
		}
		seen[c] = true
	}
	if via, err := Verify("bob", strings.ToUpper(strings.ReplaceAll(codes[3], "-", " ")), "ip"); err != nil || via != "recovery" {
		t.Fatal("recovery code (any case, spaces)", err)
	}
	if _, err := Verify("bob", codes[3], "ip"); err != ErrBadCode {
		t.Fatal("recovery code used twice")
	}
	if Status("bob").RecoveryLeft != 9 {
		t.Fatal("left")
	}
	var plain int
	Con().QueryRow("SELECT COUNT(*) FROM mfa_recovery WHERE code_hash LIKE ?", "%"+strings.ReplaceAll(codes[0], "-", "")+"%").Scan(&plain)
	if plain != 0 {
		t.Fatal("recovery code stored in clear")
	}
	// codes of one user do not work for another
	addUser(t, "eve")
	enrol(t, "eve")
	if _, err := Verify("eve", codes[4], "ip"); err != ErrBadCode {
		t.Fatal("cross-user recovery code")
	}
}

func TestMFASecretEncryptedAtRest(t *testing.T) {
	setup(t)
	withKey(t)
	pinNow(t, 1_800_000_000)
	addUser(t, "cy")
	sec, _ := enrol(t, "cy")
	raw, _ := os.ReadFile(DB)
	wal, _ := os.ReadFile(DB + "-wal")
	for _, needle := range [][]byte{sec, []byte(B32(sec)), []byte(strings.ToLower(B32(sec)))} {
		if bytes.Contains(raw, needle) || bytes.Contains(wal, needle) {
			t.Fatal("plaintext secret in auth.db")
		}
	}
	var box string
	Con().QueryRow("SELECT secret_enc FROM mfa WHERE username='cy'").Scan(&box)
	if !strings.HasPrefix(box, "v1.") {
		t.Fatal(box)
	}
	// bound to the user: the same box under another name does not decrypt
	if _, err := Open("other", "", box); err == nil {
		t.Fatal("box opens for another user")
	}
	var fid string
	Con().QueryRow("SELECT factor_id FROM mfa WHERE username='cy'").Scan(&fid)
	if _, err := Open("cy", fid, box); err != nil {
		t.Fatal("own box", err)
	}
	if _, err := Open("cy", "other-factor", box); err == nil {
		t.Fatal("box opens for another factor")
	}
	// without the key nothing verifies (fail closed)
	KeyFile = filepath.Join(t.TempDir(), "missing")
	ResetKeys()
	if _, err := Verify("cy", TOTPAt(sec, Step(nowInt())+1), "ip"); err == nil {
		t.Fatal("verified without the key")
	}
	addUser(t, "cz")
	if _, err := BeginEnrol(sess(t, "cz"), "", "ip"); err != ErrNoKey {
		t.Fatal("enrol without a key", err)
	}
}

func TestMFAKeyRotation(t *testing.T) {
	setup(t)
	old := withKey(t)
	adv := pinNow(t, 1_800_000_000)
	addUser(t, "dee")
	sec, _ := enrol(t, "dee")
	oldPath := KeyFile
	newPath := filepath.Join(t.TempDir(), "new.key")
	k := make([]byte, 32)
	rand.Read(k)
	os.WriteFile(newPath, hexs(k), 0o400) // the hex form of a key file
	KeyFile, OldKeyFile = newPath, oldPath
	ResetKeys()
	if !Status("dee").NeedsReKeying {
		t.Fatal("needs rekey")
	}
	if n, err := Rekey(); err != nil || n != 1 {
		t.Fatal("rekey", n, err)
	}
	OldKeyFile = ""
	ResetKeys()
	adv(30)
	if _, err := Verify("dee", TOTPAt(sec, Step(nowInt())), "ip"); err != nil {
		t.Fatal("after rekey", err)
	}
	_ = old
}

func mustTok(t *testing.T, s *Session) string {
	// a session's token is not stored; open an equivalent stale one for the test
	tok, _ := NewSession(s.Username, Opts{AuthAt: nowInt() - FRESH_AUTH - 5})
	return tok
}

func hexs(b []byte) []byte {
	const h = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, x := range b {
		out = append(out, h[x>>4], h[x&15])
	}
	return out
}

func TestMFADisableAndAdminReset(t *testing.T) {
	setup(t)
	withKey(t)
	adv := pinNow(t, 1_800_000_000)
	addUser(t, "fay")
	sec, _ := enrol(t, "fay")
	s := sess(t, "fay")
	if err := Disable(s, "000000", "ip"); err == nil {
		t.Fatal("disabled with a wrong code")
	}
	adv(30)
	// replacing the factor through enrolment needs a current code; a fresh password; stale sign-in refused
	if _, err := BeginEnrol(s, "", "ip"); err != ErrHasFactor {
		t.Fatal("replace without a code", err)
	}
	adv(FRESH_AUTH + 1)
	if err := Disable(GetSession(mustTok(t, s)), TOTPAt(sec, Step(nowInt())), "ip"); err != ErrStale {
		t.Fatal("disable on a stale sign-in", err)
	}
	s = sess(t, "fay")
	if err := Disable(s, TOTPAt(sec, Step(nowInt())), "ip"); err != nil || Enabled("fay") {
		t.Fatal("disable", err)
	}
	enrol(t, "fay")
	tok, _ := NewSession("fay", Opts{})
	pt, _ := NewPending("fay", "/", "local", false, "")
	if _, err := AdminReset("admin", "fay", "short", "ip", "", "totp"); err == nil {
		t.Fatal("reset without a reason")
	}
	if n, err := AdminReset("admin", "fay", "lost phone, verified by call", "ip", "abc", "totp"); err != nil || n < 2 || Enabled("fay") ||
		GetSession(tok) != nil || GetPending(pt) != nil || Status("fay").RecoveryLeft != 0 {
		t.Fatal("admin reset", n, err)
	}
	var acts []string
	for _, r := range AuditLog(20) {
		acts = append(acts, r.Actor+":"+r.Action+":"+r.Target)
	}
	j := strings.Join(acts, " ")
	if l := AuditLog(1); l[0].Reason != "lost phone, verified by call" || l[0].Method != "totp" {
		t.Fatal("audit reason/method", l[0])
	}
	for _, want := range []string{"fay:mfa.enrol:fay", "fay:mfa.disable:fay", "admin:mfa.reset:fay"} {
		if !strings.Contains(j, want) {
			t.Fatal("audit", j)
		}
	}
}

func TestPendingLogin(t *testing.T) {
	setup(t)
	withKey(t)
	adv := pinNow(t, 1_800_000_000)
	addUser(t, "gil")
	pt, err := NewPending("gil", "/backups", "local", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if GetSession(pt) != nil {
		t.Fatal("a pending login is not a session")
	}
	for i := 0; i < PENDING_TRIES; i++ {
		if !PendingTry(pt) {
			t.Fatal("try", i)
		}
	}
	if PendingTry(pt) {
		t.Fatal("too many wrong codes")
	}
	pt, _ = NewPending("gil", "/backups", "local", true, "")
	adv(PENDING_TTL + 1)
	if GetPending(pt) != nil {
		t.Fatal("expired")
	}
	pt, _ = NewPendingAt("gil", "/backups", "keycloak", true, "", nowInt()-3600)
	other, _ := NewPending("gil", "/", "local", false, "")
	if GetPending(pt) == nil || GetPending(other) == nil {
		t.Fatal("a second browser's pending login must not cancel the first")
	}
	st, p, err := FinishPending(pt, "totp", "1.2.3.4", "Firefox")
	if s := GetSession(st); s == nil || s.AuthAt != nowInt()-3600 {
		t.Fatal("the code step must not refresh the primary authentication", s)
	}
	s := GetSession(st)
	if err != nil || p.Next != "/backups" || s == nil || !s.Remember || s.MFAAt != nowInt() || s.MFAVia != "totp" {
		t.Fatal("finish", err, s)
	}
	if _, _, err := FinishPending(pt, "totp", "", ""); err == nil {
		t.Fatal("pending used twice")
	}
}

func TestSessionsRememberSlidingRotateRevoke(t *testing.T) {
	setup(t)
	adv := pinNow(t, 1_800_000_000)
	addUser(t, "hal")
	plain, _ := NewSession("hal", Opts{IP: "10.0.0.1", UA: "Mozilla/5.0 (X11; Linux x86_64) Firefox/131.0"})
	rem, _ := NewSession("hal", Opts{Remember: true})
	if s := GetSession(plain); s == nil || s.Remember || s.MaxAge() != SessionMax {
		t.Fatal("plain", s)
	}
	if s := GetSession(rem); s == nil || !s.Remember || s.MaxAge() != RememberMax {
		t.Fatal("remember")
	}
	// idle: plain dies after 2 h, remember survives 6 days of silence and slides
	adv(float64(SessionIdle) + 5)
	if GetSession(plain) != nil {
		t.Fatal("plain idle")
	}
	for i := 0; i < 4; i++ {
		adv(6 * 86400)
		if GetSession(rem) == nil {
			t.Fatal("remember idle slide", i)
		}
	}
	// absolute 30 days even when used every day
	adv(7 * 86400)
	if GetSession(rem) != nil {
		t.Fatal("remember idle 7 days")
	}
	rem, _ = NewSession("hal", Opts{Remember: true})
	for i := 0; i < 30; i++ {
		adv(86400)
		GetSession(rem)
	}
	adv(1)
	if GetSession(rem) != nil {
		t.Fatal("remember past 30 days")
	}
	// rotation keeps the kind and the start, records the factor, kills the old token, new CSRF
	a, _ := NewSession("hal", Opts{Remember: true, IP: "10.0.0.2"})
	sa := GetSession(a)
	adv(100)
	b := Rotate(a, nowInt(), "totp")
	sb := GetSession(b)
	if b == "" || b == a || GetSession(a) != nil || sb == nil || !sb.Remember || sb.Created != sa.Created || sb.MFAAt != nowInt() ||
		sb.CSRF == sa.CSRF {
		t.Fatal("rotate", sb)
	}
	// list, revoke one, revoke others, revoke all
	c, _ := NewSession("hal", Opts{UA: "curl"})
	d, _ := NewSession("hal", Opts{})
	list := UserSessions("hal", b)
	cur := 0
	for _, x := range list {
		if x.Current {
			cur++
		}
	}
	if len(list) != 3 || cur != 1 {
		t.Fatal("list", list)
	}
	if !RevokeSession("hal", GetSession(c).ID()) || GetSession(c) != nil || RevokeSession("hal", "zz") || RevokeSession("other", GetSession(d).ID()) {
		t.Fatal("revoke one")
	}
	if n := RevokeOthers("hal", b); n != 1 || GetSession(d) != nil || GetSession(b) == nil {
		t.Fatal("revoke others", n)
	}
	if RevokeAll("hal") != 1 || GetSession(b) != nil {
		t.Fatal("revoke all")
	}
	// Init keeps live remember sessions and prunes dead plain ones
	keep, _ := NewSession("hal", Opts{Remember: true})
	gone, _ := NewSession("hal", Opts{})
	adv(3 * 3600)
	if err := Init("", nil); err != nil {
		t.Fatal(err)
	}
	var n int
	Con().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n)
	if n != 1 || GetSession(keep) == nil || GetSession(gone) != nil {
		t.Fatal("init prune", n)
	}
}

// The Python rollback image inserts sessions by column name and users positionally: the additive schema must
// keep both working.
func TestSchemaIsAdditiveForPython(t *testing.T) {
	setup(t)
	if _, err := Con().Exec("INSERT INTO users VALUES (?,?,?,0,1,?)", "py", "x", "viewer", 1); err != nil {
		t.Fatal("positional users insert", err)
	}
	if _, err := Con().Exec("INSERT INTO sessions (token_hash, username, created, last_seen, csrf, id_token) VALUES (?,?,?,?,?,?)",
		"h", "py", nowInt(), nowInt(), "c", nil); err != nil {
		t.Fatal("python session insert", err)
	}
	if _, err := Con().Exec("INSERT INTO oidc_states VALUES (?,?,?,?,?)", "s", "n", "v", "/", 1); err != nil {
		t.Fatal("python oidc insert", err)
	}
	var rem sql.NullInt64
	Con().QueryRow("SELECT remember FROM sessions WHERE token_hash='h'").Scan(&rem)
	if rem.Int64 != 0 {
		t.Fatal("default remember")
	}
}

// Hermes review: enrolment races, factor-bound changes, parallel guesses.
func TestMFARacesAndBudgets(t *testing.T) {
	setup(t)
	withKey(t)
	adv := pinNow(t, 1_800_000_000)
	addUser(t, "ivy")
	attacker, victim := sess(t, "ivy"), sess(t, "ivy")
	asec, err := BeginEnrol(attacker, "", "a")
	if err != nil {
		t.Fatal(err)
	}
	vsec, err := BeginEnrol(victim, "", "v") // the victim starts later and confirms first
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmEnrol(victim, TOTPAt(vsec, Step(nowInt())), "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmEnrol(attacker, TOTPAt(asec, Step(nowInt())), "a"); err == nil {
		t.Fatal("a stale enrolment replaced the active factor")
	}
	if _, err := BeginEnrol(attacker, "", "a"); err != ErrHasFactor {
		t.Fatal("begin without a code while a factor is on", err)
	}
	// a change authorised by one factor never touches its replacement
	var fid string
	Con().QueryRow("SELECT factor_id FROM mfa WHERE username='ivy'").Scan(&fid)
	if err := wipeFactor("ivy", "old-factor"); err == nil || !Enabled("ivy") {
		t.Fatal("wiped a factor the code did not prove")
	}
	if err := wipeFactor("ivy", fid); err != nil || Enabled("ivy") {
		t.Fatal("wipe own factor", err)
	}
	enrol(t, "ivy")
	// parallel wrong codes cannot exceed the per-IP budget
	adv(30)
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() { _, err := Verify("ivy", "000001", "par"); done <- err }()
	}
	bad := 0
	for i := 0; i < 20; i++ {
		if <-done == ErrBadCode {
			bad++
		}
	}
	if bad > MAX_FAIL {
		t.Fatalf("%d guesses checked, limit %d", bad, MAX_FAIL)
	}
}

// A secret sealed under an earlier product's label opens only when TOTP_LEGACY_LABEL names that label.
func TestOpenLegacyLabel(t *testing.T) {
	withKey(t)
	k, err := loadKey(KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	a := gcm(k)
	nonce := randBytes(a.NonceSize())
	ct := a.Seal(nil, nonce, []byte("JBSWY3DPEHPK3PXP"), []byte("oldname-totp|alice|f1"))
	oldID := sha256.Sum256(append([]byte("oldname-totp-key-id|"), k...))
	box := "v1." + hex.EncodeToString(oldID[:4]) + "." + base64.RawURLEncoding.EncodeToString(append(nonce, ct...))
	old := LegacyLabel
	t.Cleanup(func() { LegacyLabel = old })
	LegacyLabel = ""
	if _, err := Open("alice", "f1", box); err == nil {
		t.Fatal("legacy secret opened without TOTP_LEGACY_LABEL")
	}
	LegacyLabel = "oldname-totp"
	plain, err := Open("alice", "f1", box)
	if err != nil || string(plain) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("legacy open: %q %v", plain, err)
	}
	if _, err := Open("bob", "f1", box); err == nil {
		t.Fatal("legacy secret opened for another user")
	}
	fresh, _ := Seal("alice", "f1", []byte("x"))
	if p, err := Open("alice", "f1", fresh); err != nil || string(p) != "x" {
		t.Fatal("current label broke with a legacy label set")
	}
}

// An admin reset marks the account for re-enrolment; only the user's own confirmed enrolment clears it.
func TestAdminResetForcesReenrol(t *testing.T) {
	setup(t)
	withKey(t)
	pinNow(t, 1_800_000_000)
	addUser(t, "gus")
	enrol(t, "gus")
	if ReenrolDue("gus") {
		t.Fatal("due before any reset")
	}
	if _, err := AdminReset("admin", "gus", "lost phone, verified by call", "ip", "", "totp"); err != nil || !ReenrolDue("gus") {
		t.Fatal("reset did not set the flag", err)
	}
	var actor string
	Con().QueryRow("SELECT actor FROM mfa_reenrol WHERE username='gus'").Scan(&actor)
	if actor != "admin" {
		t.Fatal("flag actor", actor)
	}
	s := sess(t, "gus")
	if _, err := BeginEnrol(s, "", "ip"); err != nil || !ReenrolDue("gus") {
		t.Fatal("begin alone must not clear the flag", err)
	}
	enrol(t, "gus")
	var n int
	Con().QueryRow("SELECT COUNT(*) FROM mfa_reenrol WHERE username='gus'").Scan(&n)
	if ReenrolDue("gus") || n != 0 {
		t.Fatal("confirmed enrolment did not clear the flag", n)
	}
}
