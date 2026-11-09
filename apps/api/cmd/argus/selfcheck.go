package main

// selfcheck.go: the worker job `selfcheck` (internal/selfcheck): Argus's own health every 5 minutes, written to
// selfcheck.json; one notice when Argus has been failing for ARGUS_SELFCHECK_PAGE_AFTER, one when it recovers.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/secret"
	"github.com/sinaneysi/argus/internal/selfcheck"
	"github.com/sinaneysi/argus/internal/worker"
)

// selfcheckEvery is the self-check's interval.
const selfcheckEvery = 5 * time.Minute

// credExpiryFile is ARGUS_CRED_EXPIRY_FILE, else <settings-out>/credentials.json (the file the credentials page reads).
func credExpiryFile(c *config.Config) string {
	if p := c.Values["ARGUS_CRED_EXPIRY_FILE"].Raw; p != "" {
		return p
	}
	return filepath.Join(c.SettingsOut, "credentials.json")
}

// selfcheckOptions are the self-check's inputs in the volume layout; s gives the worker's job states.
func selfcheckOptions(c *config.Config, s *worker.Scheduler, started time.Time, stderr io.Writer) *selfcheck.Options {
	v := func(k string) string { return c.Values[k].Raw }
	after, _ := strconv.ParseInt(v("ARGUS_SELFCHECK_PAGE_AFTER"), 10, 64)
	return &selfcheck.Options{
		App:         c.DisplayName,
		StateDir:    c.StateDir,
		Out:         v("ARGUS_SELFCHECK_FILE"),
		DBs:         []string{c.DB, c.BoardDB, c.AuthDB},
		AlertsFile:  v("ARGUS_ALERTS_FILE"),
		CredStatus:  v("ARGUS_CRED_STATUS_FILE"),
		CredExpiry:  credExpiryFile(c),
		SourcesFile: filepath.Join(c.SettingsOut, "sources-status.json"),
		ClaudeFile:  filepath.Join(c.SettingsOut, "claude.json"),
		AppURL:      v("ARGUS_SELFCHECK_API_URL"),
		PushURL:     v("ARGUS_DEADMAN_PUSH_URL"),
		PageAfter:   after,
		Started:     started,
		Jobs:        func() []selfcheck.Job { return jobStates(s) },
		Key:         func() selfcheck.Check { return keyCheck(c) },
		Notify: func(text string) error {
			notify.SetAppName(c.DisplayName)
			msg := notify.Formatter{Base: c.PublicURL}.Text("selfcheck", text, "")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := senderFrom(c).Send(ctx, msg, "notify:selfcheck")
			if err != nil && !errors.Is(err, notify.ErrNotSent) {
				fmt.Fprintln(stderr, "warning: self-check notice not sent:", err)
				return err
			}
			return nil
		},
	}
}

// selfcheckJob is the worker job; it reads the scheduler's own job states.
func selfcheckJob(c *config.Config, s *worker.Scheduler, stderr io.Writer) worker.Job {
	o := selfcheckOptions(c, s, time.Now(), stderr)
	return worker.Job{Name: "selfcheck", Every: selfcheckEvery, Run: func(ctx context.Context) error {
		_, err := o.Run(ctx)
		return err
	}}
}

func jobStates(s *worker.Scheduler) []selfcheck.Job {
	var out []selfcheck.Job
	for _, st := range s.States() {
		every, _ := time.ParseDuration(st.Every)
		out = append(out, selfcheck.Job{Name: st.Name, Enabled: st.Enabled, Reason: st.Reason, Every: every,
			LastStart: st.LastStart, LastEnd: st.LastEnd, LastError: st.LastError, Running: st.Running})
	}
	return out
}

// keyCheck: the master key is set (or its file exists) and opens every sealed secret of the volume. Names only.
func keyCheck(c *config.Config) selfcheck.Check {
	get := func(k string) string { return c.Values[k].Raw }
	ck := func(st, d string) selfcheck.Check {
		return selfcheck.Check{Name: "master key", Group: selfcheck.Group, Status: st, Detail: d}
	}
	k, src, err := secret.ForVolume(get, false)
	if err != nil {
		return ck("FAIL", err.Error())
	}
	if k == nil {
		return ck("OK", "not used (legacy layout)")
	}
	if strings.HasPrefix(src, "/") {
		src = "key file"
	}
	opened, plain, bad := secret.FilesOf(get).Audit(k)
	switch {
	case len(bad) > 0:
		names := bad
		if len(names) > 3 {
			names = append(names[:3:3], "...")
		}
		return ck("FAIL", fmt.Sprintf("%d secret(s) do not open with the master key (%s): set the key they were written with", len(bad), strings.Join(names, ", ")))
	case plain > 0:
		return ck("WARN", fmt.Sprintf("from %s; %d secret(s) still plaintext: run argus secrets rekey", src, plain))
	}
	return ck("OK", fmt.Sprintf("from %s, opens %d secret(s)", src, opened))
}
