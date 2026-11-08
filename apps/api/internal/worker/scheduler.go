// Package worker is the in-process scheduler of `argus worker` (ADR-0021 D3): every job runs on its own interval
// inside the worker, replacing the systemd timers of the legacy layout. A job runs only when the data sources it
// needs are configured; one run per job at a time (a tick that finds the previous run still going is skipped).
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Job is one scheduled job.
type Job struct {
	Name    string
	Every   time.Duration
	Sources []string // data-source types the job needs (ADR-0021 D5); listed when it is not configured
	// Ready says whether the job can run; "" reason means ready. nil: always ready.
	Ready func() (ok bool, reason string)
	Run   func(ctx context.Context) error
}

// State is what the scheduler knows of one job.
type State struct {
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Reason    string    `json:"reason,omitempty"`
	Every     string    `json:"every"`
	LastStart time.Time `json:"last_start,omitempty"`
	LastEnd   time.Time `json:"last_end,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	Runs      int       `json:"runs"`
	Skipped   int       `json:"skipped"`
	running   bool
	nextAt    time.Time
}

// Scheduler runs jobs.
type Scheduler struct {
	Log  *slog.Logger
	Tick time.Duration // how often due jobs are checked (default 1s)
	Now  func() time.Time

	mu    sync.Mutex
	jobs  []Job
	state map[string]*State
	wg    sync.WaitGroup
}

// Add registers a job; it first runs at the first tick.
func (s *Scheduler) Add(j Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		s.state = map[string]*State{}
	}
	s.jobs = append(s.jobs, j)
	s.state[j.Name] = &State{Name: j.Name, Every: j.Every.String()}
}

// States is a copy of every job's state, by name.
func (s *Scheduler) States() []State {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []State
	for _, st := range s.state {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Step starts every job that is due and ready; it does not wait for them (Wait does).
func (s *Scheduler) Step(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		st := s.state[j.Name]
		if now.Before(st.nextAt) {
			continue
		}
		st.nextAt = now.Add(j.Every)
		ok, reason := true, ""
		if j.Ready != nil {
			ok, reason = j.Ready()
		}
		if ok != st.Enabled || reason != st.Reason {
			st.Enabled, st.Reason = ok, reason
			if s.Log != nil {
				if ok {
					s.Log.Info("job enabled", "job", j.Name, "every", j.Every.String())
				} else {
					s.Log.Info("job not configured", "job", j.Name, "reason", reason, "sources", j.Sources)
				}
			}
		}
		if !ok {
			continue
		}
		if st.running {
			st.Skipped++
			if s.Log != nil {
				s.Log.Warn("job still running: tick skipped", "job", j.Name)
			}
			continue
		}
		st.running, st.LastStart = true, now
		s.wg.Add(1)
		go func(j Job, st *State) {
			defer s.wg.Done()
			err := func() (err error) {
				defer func() {
					if p := recover(); p != nil {
						err = panicErr{p}
					}
				}()
				return j.Run(ctx)
			}()
			s.mu.Lock()
			st.running, st.LastEnd, st.Runs = false, s.now(), st.Runs+1
			st.LastError = ""
			if err != nil {
				st.LastError = err.Error()
			}
			s.mu.Unlock()
			if err != nil && s.Log != nil {
				s.Log.Error("job failed", "job", j.Name, "error", err.Error())
			}
		}(j, st)
	}
}

type panicErr struct{ v any }

func (p panicErr) Error() string { return fmt.Sprint("panic: ", p.v) }

// Wait blocks until every started run has returned.
func (s *Scheduler) Wait() { s.wg.Wait() }

// Run steps until ctx ends, then waits for the running jobs.
func (s *Scheduler) Run(ctx context.Context) {
	tick := s.Tick
	if tick <= 0 {
		tick = time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	s.Step(ctx)
	for {
		select {
		case <-ctx.Done():
			s.Wait()
			return
		case <-t.C:
			s.Step(ctx)
		}
	}
}
