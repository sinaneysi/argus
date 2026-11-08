package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

// fakeNow pins Now() to the local time of a 14-digit request timestamp plus off seconds.
func fakeNow(t *testing.T, stamp string, off float64) {
	t.Helper()
	at, err := time.ParseInLocation("20060102150405", stamp, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	old := NowFn
	NowFn = func() float64 { return float64(at.Unix()) + off }
	t.Cleanup(func() { NowFn = old })
}

func boardDropReq(t *testing.T, rid string) {
	t.Helper()
	d := filepath.Join(SET_IN, "board-requests")
	os.MkdirAll(d, 0o755)
	if err := os.WriteFile(filepath.Join(d, rid+".json"), []byte(`{"kind":"move"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRequestAge(t *testing.T) {
	fakeNow(t, "20261108125805", 30)
	if a, ok := requestAge("20261108125805-abcdef12"); !ok || a != 30 {
		t.Fatal(a, ok)
	}
	at, _ := time.ParseInLocation("20060102150405", "20261108125805", time.Local)
	if a, ok := requestAge(I2S(at.Unix()-5) + "-abcdef12"); !ok || a != 35 {
		t.Fatal(a, ok)
	}
	if _, ok := requestAge("x-abcdef12"); ok {
		t.Fatal("no timestamp")
	}
}

func TestBoardRequestBannerFresh(t *testing.T) {
	e := boardSetup(t)
	rid := "20261108125805-abcdef12"
	boardDropReq(t, rid)
	e.json(t, "out/board-status.json", map[string]any{"last_run": float64(time.Now().Unix())})
	fakeNow(t, "20261108125805", 3)
	c := e.ctx(e.view)
	if b := BoardRequestBanner(c, py.NewQS("req", rid)); !strings.Contains(b, "banner ok") || !strings.Contains(b, "The board sync applies it") || c.Refresh != 2 {
		t.Fatal(b, c.Refresh)
	}
	fakeNow(t, "20261108125805", 20)
	c = e.ctx(e.view)
	if b := BoardRequestBanner(c, py.NewQS("req", rid)); !strings.Contains(b, "banner ok") || c.Refresh != true {
		t.Fatal(b, c.Refresh)
	}
	if r := apiRefresh(c); r["seconds"] != 5 {
		t.Fatal(r)
	}
}

func TestBoardRequestBannerStale(t *testing.T) {
	e := boardSetup(t)
	rid := "20261108125805-abcdef12"
	boardDropReq(t, rid)
	fakeNow(t, "20261108125805", 61)
	c := e.ctx(e.view)
	b := BoardRequestBanner(c, py.NewQS("req", rid))
	if !strings.Contains(b, "banner fail") || !strings.Contains(b, "Not applied: the board sync has not picked this up after 61 s") ||
		!strings.Contains(b, "Nothing changed in GitLab or Jira.") || c.Refresh != nil {
		t.Fatal(b, c.Refresh)
	}
	// The sync's last run is 20 min old: give up after the short first wait, not after a minute.
	fakeNow(t, "20261108125805", 15)
	e.json(t, "out/board-status.json", map[string]any{"last_run": Now() - 1200})
	c = e.ctx(e.view)
	if b := BoardRequestBanner(c, py.NewQS("req", rid)); !strings.Contains(b, "banner fail") || c.Refresh != nil {
		t.Fatal(b, c.Refresh)
	}
	// Taken by the sync but no result after 5 min: stop waiting too.
	os.Remove(filepath.Join(SET_IN, "board-requests", rid+".json"))
	fakeNow(t, "20261108125805", 400)
	c = e.ctx(e.view)
	if b := BoardRequestBanner(c, py.NewQS("req", rid)); !strings.Contains(b, "No result") || c.Refresh != nil {
		t.Fatal(b, c.Refresh)
	}
}

func TestBoardRequestBannerProcessedUnchanged(t *testing.T) {
	e := boardSetup(t)
	rid := "20261108125805-abcdef12"
	e.json(t, "out/board-status.json", map[string]any{"last_run": float64(0), "requests": map[string]any{rid: map[string]any{"result": "moved"}}})
	fakeNow(t, "20261108125805", 3600) // an old link to a request that was applied still says what happened
	c := e.ctx(e.view)
	if b := BoardRequestBanner(c, py.NewQS("req", rid)); !strings.Contains(b, "Moved.") || c.Refresh != nil {
		t.Fatal(b, c.Refresh)
	}
}

func TestProblemAndIdeaSentBannersGiveUp(t *testing.T) {
	boardSetup(t)
	at, _ := time.ParseInLocation("20060102150405", "20261108125805", time.Local)
	prid := I2S(at.Unix()) + "-abcdef12"
	os.MkdirAll(filepath.Join(SET_IN, "problem-actions"), 0o755)
	os.WriteFile(filepath.Join(SET_IN, "problem-actions", prid+".json"), []byte("{}"), 0o644)
	irid := "20261108125805-abcdef12"
	os.MkdirAll(filepath.Join(SET_IN, "idea-actions"), 0o755)
	os.WriteFile(filepath.Join(SET_IN, "idea-actions", irid+".json"), []byte("{}"), 0o644)

	fakeNow(t, "20261108125805", 5)
	c := NewCtx(nil, "/problems")
	if b := probSentBanner(c, py.NewQS("sent", "problem", "preq", prid)); !strings.Contains(b, "Sent. Argus applies it") || c.Refresh != 2 {
		t.Fatal(b, c.Refresh)
	}
	c = NewCtx(nil, "/review")
	if b := ideasSentBanner(c, py.NewQS("sent", "accept", "req", irid)); !strings.Contains(b, "Sent. Argus applies it") || c.Refresh != 2 {
		t.Fatal(b, c.Refresh)
	}
	fakeNow(t, "20261108125805", 90)
	c = NewCtx(nil, "/problems")
	if b := probSentBanner(c, py.NewQS("sent", "problem", "preq", prid)); !strings.Contains(b, "Not applied: Argus has not picked this up") || c.Refresh != nil {
		t.Fatal(b, c.Refresh)
	}
	c = NewCtx(nil, "/review")
	if b := ideasSentBanner(c, py.NewQS("sent", "accept", "req", irid)); !strings.Contains(b, "banner fail") || c.Refresh != nil {
		t.Fatal(b, c.Refresh)
	}
}
