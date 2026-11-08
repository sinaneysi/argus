// Command argus-api is Argus's web app (the Go port of app/server.py): a read-only view of the argus
// SQLite history with login, sessions, Keycloak SSO and the admin forms that write request files for root.
//
// Configuration: every setting is read through internal/config (the environment, then /etc/argus/argus.env or
// $ARGUS_CONFIG, then the schema default). The canonical names are ARGUS_* (ARGUS_DB, ARGUS_BIND, ARGUS_ALERTS_FILE,
// ...); the old OPS_* names still work and print one deprecation warning each at start. An invalid configuration
// stops the server before it listens, with every problem listed. `argus config show` prints the effective values.
//
// `argus-api healthcheck URL` is the container health check (no shell, no curl in the image).
// Break-glass (root on the Argus host: docker exec argus-api /app/argus-api ...), audited as cli:root:
//
//	argus-api user reset-2fa <user> <reason>   remove the user's 2FA, end all their sessions; they must enrol at next sign-in
//	argus-api user signout <user>              end all the user's sessions
//	argus-api mfa rekey                        re-encrypt the 2FA secrets with TOTP_KEY_FILE (old: TOTP_KEY_FILE_OLD)
//	argus-api mfa status                       2FA state of every user (no secrets)
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/app"
	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/oidc"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "healthcheck" {
		c := http.Client{Timeout: 2 * time.Second}
		r, err := c.Get(os.Args[2])
		if err != nil || r.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) >= 3 && (os.Args[1] == "user" || os.Args[1] == "mfa") {
		if err := auth.Init("", nil); err != nil {
			fmt.Fprintln(os.Stderr, "auth.db:", err)
			os.Exit(1)
		}
		os.Exit(cli(os.Args[1:]))
	}
	cfg, ok := loadConfig(os.Stderr)
	if !ok {
		os.Exit(1)
	}
	if err := migrate.OnStart(context.Background(), cfg, func(f string, a ...any) { fmt.Printf(f+"\n", a...) }); err != nil {
		fmt.Fprintln(os.Stderr, "argus-api:", err)
		os.Exit(1)
	}
	if err := auth.Init(app.IMPORT_HTPASSWD, app.ADMINS); err != nil {
		fmt.Fprintln(os.Stderr, "auth.db:", err)
		os.Exit(1)
	}
	auth.BootstrapAdmin(cfg.AdminUser, cfg.AdminInitialPassword)
	if app.DEMO { // demo mode: no single sign-on, no outbound connection at all
		oidc.ConfigureWith("", "", "", "", "")
		fmt.Println("demo mode: sample data, every write refused")
	} else {
		oidc.ConfigureFromEnv()
		oidc.Prefetch()
	}
	if oidc.Configured() {
		fmt.Printf("single sign-on: Keycloak %s client %s\n", oidc.ISSUER, oidc.CLIENT_ID)
	} else {
		fmt.Println("single sign-on: off")
	}
	app.PreloadStatic()
	i := strings.LastIndex(app.BIND, ":")
	host, port := app.BIND[:i], app.BIND[i+1:]
	fmt.Printf("argus-api on http://%s:%s db=%s auth=%s\n", host, port, app.DB, auth.DB)
	srv := &http.Server{
		Addr:              net.JoinHostPort(host, port),
		Handler:           app.Handler{},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// loadConfig prints the configuration's warnings (each deprecated name once) and, when it is invalid, every
// problem; ok is false then and the server must not start.
func loadConfig(stderr io.Writer) (*config.Config, bool) {
	cfg, err := config.Process()
	for _, w := range cfg.Warnings {
		fmt.Fprintln(stderr, "config warning:", w)
	}
	if errs, isList := err.(config.Errors); isList {
		for _, e := range errs {
			fmt.Fprintln(stderr, "CONFIG ERROR:", e)
		}
		fmt.Fprintf(stderr, "argus-api: invalid configuration, %d problem(s); not starting\n", len(errs))
		return cfg, false
	} else if err != nil {
		fmt.Fprintln(stderr, "CONFIG ERROR:", err)
		return cfg, false
	}
	return cfg, true
}

func cli(a []string) int {
	exists := func(u string) bool {
		for _, x := range auth.Users() {
			if x.Username == u {
				return true
			}
		}
		return false
	}
	switch {
	case a[0] == "user" && a[1] == "reset-2fa" && len(a) >= 4:
		if !exists(a[2]) {
			fmt.Fprintln(os.Stderr, "no such user")
			return 1
		}
		n, err := auth.AdminReset("cli:root", a[2], strings.Join(a[3:], " "), "", "", "cli")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("2FA of %s removed, %d sessions ended; they must set up 2FA at their next sign-in (audited as cli:root)\n", a[2], n)
		return 0
	case a[0] == "user" && a[1] == "signout" && len(a) == 3:
		if !exists(a[2]) {
			fmt.Fprintln(os.Stderr, "no such user")
			return 1
		}
		n := auth.RevokeAll(a[2])
		auth.Log(auth.Event{Actor: "cli:root", Action: "sessions.revoke-all", Target: a[2], Detail: fmt.Sprint(n, " ended"), Method: "cli"})
		fmt.Printf("%s signed out: %d sessions\n", a[2], n)
		return 0
	case a[0] == "mfa" && a[1] == "rekey":
		n, err := auth.Rekey()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("re-encrypted %d secrets\n", n)
		return 0
	case a[0] == "mfa" && a[1] == "status":
		fmt.Printf("key file: %s ready=%v\n", auth.KeyFile, auth.KeyReady())
		for _, u := range auth.Users() {
			st := auth.Status(u.Username)
			fmt.Printf("%-24s 2fa=%-5v recovery_left=%d rekey=%v\n", u.Username, st.Enabled, st.RecoveryLeft, st.NeedsReKeying)
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "usage: argus-api user reset-2fa <user> <reason> | user signout <user> | mfa rekey | mfa status")
	return 2
}
