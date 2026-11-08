package app

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// A page that has just sent a request file (board, problem or idea action) refreshes itself until a job takes the
// file. It must not wait forever: when nothing picks the request up, the page stops and says so.

const (
	requestFirstWait = 10  // seconds: a request this young gets a short first refresh (2 s instead of 5 s)
	requestGiveUp    = 60  // seconds: a request file still unread after this is reported as not applied
	requestJobStale  = 600 // seconds: a job whose last run is older than this is not running
	requestNoResult  = 300 // seconds: a request a job took but has reported no result for
)

// requestAge is how old a request is, from the timestamp its id starts with: 14 digits of local time
// (20261108125805-…) or 10 digits of Unix seconds. ok is false for an id without one.
func requestAge(rid string) (float64, bool) {
	head, _, _ := strings.Cut(strings.TrimSpace(rid), "-")
	var at float64
	switch len(head) {
	case 14:
		t, err := time.ParseInLocation("20060102150405", head, time.Local)
		if err != nil {
			return 0, false
		}
		at = float64(t.Unix())
	case 10:
		n, err := strconv.ParseInt(head, 10, 64)
		if err != nil {
			return 0, false
		}
		at = float64(n)
	default:
		return 0, false
	}
	age := Now() - at
	if age < 0 {
		age = 0
	}
	return age, true
}

// requestWaitBanner is the banner of a request whose file is still waiting at path. job names what consumes it
// ("the board sync"), unchanged says what a failure left alone ("Nothing changed."), lastRun is the job's last run
// (Unix seconds, 0 when unknown). While the request is fresh the page refreshes (2 s first, then 5 s); once it is
// stale it stops and shows a failure. ok is false when the file is gone (the job took it): the caller shows the result.
func requestWaitBanner(c *Ctx, path, rid, job, unchanged string, lastRun float64) (string, bool) {
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	age, known := requestAge(rid)
	jobDead := lastRun > 0 && Now()-lastRun > requestJobStale
	if known && (age >= requestGiveUp || (jobDead && age >= requestFirstWait)) {
		return Banner("Not applied: "+job+" has not picked this up after "+strconv.Itoa(int(age))+" s. "+unchanged+" "+
			"Check that "+job+" runs.", "fail"), true
	}
	if known && age < requestFirstWait {
		c.Refresh = 2
	} else {
		c.Refresh = true
	}
	return Banner("Sent. " + upperFirst(job) + " applies it within seconds; this page refreshes itself."), true
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
