package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) {
	t.Helper()
	DB = filepath.Join(t.TempDir(), "auth.db")
	Reset()
	if err := Init("", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Reset)
}

// Hashes written by the Python app (hashlib.scrypt) and by glibc crypt (the imported htpasswd) must verify.
func TestCheckPWPythonVectors(t *testing.T) {
	py := "scrypt$16384$8$1$00112233445566778899aabbccddeeff$8cc0de9218d3d6723ae203a934c046cf2bb9aa70723c2cf4b1cf06ca35c89adb"
	if !CheckPW("correct horse battery", py) || CheckPW("correct horse batterY", py) {
		t.Fatal("scrypt vector")
	}
	vec := map[string]string{
		"$6$abcdefgh$4UPJf3qf71iDb8DDBG5r.Jxy3OKpyf7on.yGTrfW2z7BZBicRPXkes.VuTPq3aS6gF63ohT6G9SJv9LejkZLu/":                      "Hello world!",
		"$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1":                    "Hello world!",
		"$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v.": "Hello world!",
		"$6$xyzXYZ12$CGZ8csjg8qPMNgLB8fB.OYL3TIWFGJhISP8FwRTZlxjoLav9lGnEXuVQYnxO2u8jfBV794DYMiapXFvveWNqZ/":                      strings.Repeat("pässwörd-long-", 6),
	}
	for h, pw := range vec {
		if !CheckPW(pw, h) {
			t.Errorf("sha512-crypt %s", h[:20])
		}
		if CheckPW(pw+"x", h) {
			t.Errorf("wrong password accepted for %s", h[:20])
		}
	}
	for _, bad := range []string{"", "plain", "scrypt$1$2", "scrypt$16384$8$1$zz$00", "scrypt$1099511627776$8$1$00$00",
		"$6$rounds=999999999$x$y", "$1$abc$def"} {
		if CheckPW("x", bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestHashRoundTripFormat(t *testing.T) {
	h, err := HashPW("a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	p := strings.Split(h, "$")
	if len(p) != 6 || p[0] != "scrypt" || p[1] != "16384" || p[2] != "8" || p[3] != "1" || len(p[4]) != 32 || len(p[5]) != 64 {
		t.Fatalf("format %s", h)
	}
	if !CheckPW("a long enough password", h) {
		t.Fatal("round trip")
	}
}

func TestLoginLockoutAndUpgrade(t *testing.T) {
	setup(t)
	// an imported htpasswd user: sha512-crypt until the first login, then scrypt
	hp := filepath.Join(t.TempDir(), "htpasswd")
	os.WriteFile(hp, []byte("old:$6$abcdefgh$4UPJf3qf71iDb8DDBG5r.Jxy3OKpyf7on.yGTrfW2z7BZBicRPXkes.VuTPq3aS6gF63ohT6G9SJv9LejkZLu/\nbad:{SHA}x\n"), 0o600)
	if err := Init(hp, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	us := Users()
	if len(us) != 1 || us[0].Username != "old" || us[0].Role != "admin" {
		t.Fatalf("import %+v", us)
	}
	tok := Login("old", "Hello world!", "1.2.3.4")
	if tok == "" || len(tok) != 43 {
		t.Fatalf("token %q", tok)
	}
	var h string
	Con().QueryRow("SELECT hash FROM users WHERE username='old'").Scan(&h)
	if !strings.HasPrefix(h, "scrypt$") {
		t.Fatal("hash not upgraded")
	}
	s := GetSession(tok)
	if s == nil || s.Username != "old" || s.Role != "admin" || len(s.CSRF) != 32 || s.Source != "local" {
		t.Fatalf("session %+v", s)
	}
	// only the SHA-256 of the token is stored
	var n int
	Con().QueryRow("SELECT COUNT(*) FROM sessions WHERE token_hash=?", tok).Scan(&n)
	if n != 0 {
		t.Fatal("raw token stored")
	}
	// 5 failures lock user+IP; the right password is then refused; another IP still works
	for i := 0; i < 5; i++ {
		if Login("old", "wrong", "9.9.9.9") != "" {
			t.Fatal("wrong password accepted")
		}
	}
	if Login("old", "Hello world!", "9.9.9.9") != "" {
		t.Fatal("locked pair logged in")
	}
	if Login("old", "Hello world!", "1.2.3.4") == "" {
		t.Fatal("other IP locked")
	}
	if Login("nobody", "x", "1.2.3.4") != "" {
		t.Fatal("unknown user")
	}
}

func TestSessionExpiryAndDisable(t *testing.T) {
	setup(t)
	pw, _ := AddUser("viewer1", "viewer")
	tok := Login("viewer1", pw, "ip")
	s := GetSession(tok)
	if s == nil || s.MustChange != 1 {
		t.Fatal("must_change")
	}
	if msg := ChangePassword("viewer1", pw, "short"); !strings.Contains(msg, "at least 12") {
		t.Fatal(msg)
	}
	if msg := ChangePassword("viewer1", pw, pw); msg == "" {
		t.Fatal("same password accepted")
	}
	if msg := ChangePassword("viewer1", "wrong", "a new long password"); msg != "Current password is wrong." {
		t.Fatal(msg)
	}
	if msg := ChangePassword("viewer1", pw, "a new long password"); msg != "" {
		t.Fatal(msg)
	}
	if GetSession(tok).MustChange != 0 {
		t.Fatal("must_change kept")
	}
	// idle timeout
	real := Now
	defer func() { Now = real }()
	base := Now()
	Now = func() float64 { return base + IDLE + 5 }
	if GetSession(tok) != nil {
		t.Fatal("idle session alive")
	}
	Now = real
	tok = Login("viewer1", "a new long password", "ip")
	SetDisabled("viewer1", true)
	if GetSession(tok) != nil || Login("viewer1", "a new long password", "ip") != "" {
		t.Fatal("disabled user")
	}
}

func TestSSOLoginRules(t *testing.T) {
	setup(t)
	tok, why := SSOLogin("kc.user", "sub-1", "viewer", "idt", []string{"admin"})
	if tok == "" || why != "" {
		t.Fatal(why)
	}
	if s := GetSession(tok); s == nil || s.Source != "keycloak" {
		t.Fatal("source")
	}
	if Logout(tok) != "idt" || GetSession(tok) != nil {
		t.Fatal("logout id_token")
	}
	if _, why := SSOLogin("kc.user", "sub-2", "viewer", "", nil); !strings.Contains(why, "another Keycloak user") {
		t.Fatal("subject binding:", why)
	}
	// a keycloak account has no password
	if Login("kc.user", "", "ip") != "" {
		t.Fatal("empty password login")
	}
	// the reserved break-glass account cannot be taken over by SSO
	BootstrapAdmin("admin", "initial-password-long")
	if _, why := SSOLogin("admin", "sub-3", "admin", "", []string{"admin"}); !strings.Contains(why, "reserved") {
		t.Fatal("reserved:", why)
	}
	SetDisabled("kc.user", true)
	if _, why := SSOLogin("kc.user", "sub-1", "viewer", "", nil); !strings.Contains(why, "disabled") {
		t.Fatal("disabled:", why)
	}
	// a local account becomes keycloak (password removed)
	AddUser("local1", "viewer")
	if tok, _ := SSOLogin("local1", "s", "admin", "", nil); tok == "" {
		t.Fatal("convert")
	}
	var h string
	Con().QueryRow("SELECT hash FROM users WHERE username='local1'").Scan(&h)
	if h != "" || Source("local1") != "keycloak" {
		t.Fatal("not converted")
	}
}

func TestOIDCStateSingleUse(t *testing.T) {
	setup(t)
	if err := OIDCPut("h1", "n", "v", "/next"); err != nil {
		t.Fatal(err)
	}
	st := OIDCTake("h1")
	if st == nil || st.Nonce != "n" || st.Verifier != "v" || st.Next != "/next" {
		t.Fatal("take")
	}
	if OIDCTake("h1") != nil {
		t.Fatal("replay")
	}
	for i := 0; i < MAX_STATES+20; i++ {
		OIDCPut("x"+string(rune('a'+i%26))+strings.Repeat("y", i/26), "n", "v", "/")
	}
	var n int
	Con().QueryRow("SELECT COUNT(*) FROM oidc_states").Scan(&n)
	if n > MAX_STATES {
		t.Fatalf("states %d", n)
	}
}

func TestBootstrapAdmin(t *testing.T) {
	setup(t)
	if BootstrapAdmin("admin", "short") {
		t.Fatal("short password")
	}
	if !BootstrapAdmin("admin", "initial-password-long") || BootstrapAdmin("admin", "another-password-long") {
		t.Fatal("bootstrap twice")
	}
	tok := Login("admin", "initial-password-long", "ip")
	if s := GetSession(tok); s == nil || s.MustChange != 1 || s.Role != "admin" {
		t.Fatal("bootstrap admin")
	}
}
