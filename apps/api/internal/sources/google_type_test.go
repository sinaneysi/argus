package sources

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func googleKey(tokenURI string) string {
	pk, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(pk)
	b, _ := json.Marshal(map[string]string{"client_email": "reader@example-project.iam.gserviceaccount.com", "token_uri": tokenURI,
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	return string(b)
}

func TestCheckGoogle(t *testing.T) {
	testAllowLocal = true
	defer func() { testAllowLocal = false }()
	sites := `{"siteEntry": [{"siteUrl": "sc-domain:example.com", "permissionLevel": "siteRestrictedUser"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/token":
			io.WriteString(w, `{"access_token": "fake-access-0002"}`)
		case r.URL.Path == "/webmasters/v3/sites" && r.Header.Get("Authorization") == "Bearer fake-access-0002":
			io.WriteString(w, sites)
		default:
			w.WriteHeader(403)
		}
	}))
	defer srv.Close()
	c, _ := checkGoogle(srv.URL, googleKey(srv.URL+"/token"), nil)
	if c.Status != "ok" || !strings.Contains(c.Detail, "1 Search Console property") || !strings.HasPrefix(c.Identity, "reader@") {
		t.Fatalf("%+v", c)
	}
	sites = `{}`
	if c, _ := checkGoogle(srv.URL, googleKey(srv.URL+"/token"), nil); c.Status != "failed" || !strings.Contains(c.Detail, "Restricted user") {
		t.Fatalf("%+v", c)
	}
	if c, _ := checkGoogle(srv.URL, "not json", nil); c.Status != "failed" || strings.Contains(c.Detail, "not json") {
		t.Fatalf("%+v", c)
	}
	if c, _ := checkGoogle(srv.URL, googleKey(srv.URL+"/nothing"), nil); c.Status == "ok" {
		t.Fatalf("%+v", c)
	}
}
