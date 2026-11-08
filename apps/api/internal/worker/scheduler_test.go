package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerIntervalsReadinessOverlap(t *testing.T) {
	now := time.Unix(1000, 0)
	s := &Scheduler{Now: func() time.Time { return now }}
	var fast, slow, off int32
	release := make(chan struct{})
	s.Add(Job{Name: "fast", Every: 10 * time.Second, Run: func(context.Context) error { atomic.AddInt32(&fast, 1); return nil }})
	s.Add(Job{Name: "slow", Every: time.Second, Run: func(context.Context) error { atomic.AddInt32(&slow, 1); <-release; return errors.New("boom") }})
	s.Add(Job{Name: "off", Every: time.Second, Sources: []string{"gitlab"}, Ready: func() (bool, string) { return false, "connect GitLab" },
		Run: func(context.Context) error { atomic.AddInt32(&off, 1); return nil }})
	ctx := context.Background()
	s.Step(ctx) // all due: fast and slow start, off is not configured
	for i := 0; i < 3; i++ {
		now = now.Add(time.Second)
		s.Step(ctx) // slow still running: skipped; fast not due
	}
	close(release)
	s.Wait()
	if fast != 1 || slow != 1 || off != 0 {
		t.Fatalf("runs fast=%d slow=%d off=%d", fast, slow, off)
	}
	st := map[string]State{}
	for _, x := range s.States() {
		st[x.Name] = x
	}
	if st["slow"].Skipped != 3 || st["slow"].LastError != "boom" || st["off"].Enabled || st["off"].Reason != "connect GitLab" {
		t.Fatalf("states %+v", st)
	}
	now = now.Add(10 * time.Second)
	s.Step(ctx)
	s.Wait()
	if fast != 2 {
		t.Fatalf("fast after its interval: %d", fast)
	}
}

func TestPanicIsAnError(t *testing.T) {
	s := &Scheduler{}
	s.Add(Job{Name: "p", Every: time.Minute, Run: func(context.Context) error { panic("bad") }})
	s.Step(context.Background())
	s.Wait()
	if s.States()[0].LastError != "panic: bad" {
		t.Fatalf("%+v", s.States())
	}
}
