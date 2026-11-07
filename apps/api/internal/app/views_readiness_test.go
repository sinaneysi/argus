package app

import (
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

func TestReadinessWindowAndSection(t *testing.T) {
	e := inventorySetup(t)
	e.exec(t, `CREATE TABLE incidents (id INTEGER PRIMARY KEY, status TEXT, severity TEXT)`)
	e.exec(t, "INSERT INTO incidents VALUES (1, 'open', 'critical')")
	old := ReadinessToday
	t.Cleanup(func() { ReadinessToday = old })
	day := func(s string) func() time.Time {
		return func() time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	}
	cfg := merged(READINESS, nil)
	for today, want := range map[string]string{"2026-10-27": "D-31 out", "2026-10-28": "D-30 in", "2026-11-25": "D-2 freeze",
		"2026-11-27": "D-day freeze", "2026-11-29": "D+2 in", "2026-11-30": "D+3 out"} {
		w := readinessWindow(cfg, day(today)())
		got := readinessDLabel(w.d) + If(w.freeze, " freeze", If(w.in, " in", " out"))
		if got != want {
			t.Errorf("%s: %s != %s", today, got, want)
		}
	}
	ReadinessToday = day("2026-10-01")

	ReadinessToday = day("2026-11-24")
	e.write(t, "readiness.json", `{"at": 1, "deploys": {"days": [{"day": "2026-11-24", "release": 2}]},
	  "edge429": {"total": 12345, "requests": 1000000, "top_hosts": [["<h>", 3]]},
	  "pages": [{"url": "https://www.shop.northwind.example/x", "status": 200, "time": 6.25}], "errors": ["e1"]}`)

	_ = py.QS{}
}
