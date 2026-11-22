package capability

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// userInfo is a fake user:password part of a URL, built at run time.
var userInfo = "u" + ":" + "p" + "@"

// testServer answers every host name: the transport dials the test listener whatever the URL says, so the
// capability sees real host names (an IP literal is refused by design).
type testServer struct {
	srv   *httptest.Server
	calls atomic.Int32
}

func newTestServer(t *testing.T) *testServer {
	ts := &testServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("BODY-MARKER-do-not-keep")) })
	mux.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.example.test/ok", http.StatusFound)
	})
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.calls.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *testServer) transport() http.RoundTripper {
	addr := ts.srv.Listener.Addr().String()
	return &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}
}

// writeCap is a fake Write-tier capability for the refusal rules.
type writeCap struct{ *HTTPCheck }

func (writeCap) Name() string { return "test.write" }
func (writeCap) Tier() Tier   { return Write }
func (writeCap) Schema() Schema {
	return Schema{Fields: []Field{{Name: "key", Type: String, Required: true}, {Name: "value_ref", Type: String, Secret: true}}}
}
func (w writeCap) Validate(a Args, _ Env) error { return w.Schema().Check(a) }
