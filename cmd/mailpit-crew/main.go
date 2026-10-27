// Command mailpit-crew is an admin panel for Mailpit.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/audit"
	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/config"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"github.com/mddanish/mailpit-crew/internal/mailpit"
	"github.com/mddanish/mailpit-crew/internal/managed"
	"github.com/mddanish/mailpit-crew/internal/restart"
	"github.com/mddanish/mailpit-crew/internal/users"
	"github.com/mddanish/mailpit-crew/internal/web"
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "dev"

const usage = `MailPit-Crew %s - installs, configures and runs Mailpit (unofficial)

Usage:
  mailpit-crew [flags]                         run Crew, which runs Mailpit
  mailpit-crew import [flags] <mailpit.ini>    import an existing Mailpit setup
  mailpit-crew reset-password [flags]          set a new random password for the Crew admin
  mailpit-crew hash-password                   read a password from stdin, print a bcrypt hash
  mailpit-crew version                         print the version

Run "mailpit-crew --help" to list all flags.
`

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version":
			fmt.Println("mailpit-crew", version)
			return
		case "reset-password":
			os.Exit(resetPassword(args[1:]))
		case "hash-password":
			os.Exit(hashPassword())
		case "import":
			os.Exit(importSetup(args[1:]))
		case "help":
			fmt.Printf(usage, version)
			return
		}
	}
	if err := serve(args); err != nil {
		if errors.Is(err, config.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

func resetPassword(args []string) int {
	user := users.AdminName
	if len(args) > 0 && !strings.HasPrefix(args[len(args)-1], "-") {
		user, args = args[len(args)-1], args[:len(args)-1]
	}
	cfg, err := config.Load(args, os.Stderr)
	if err != nil {
		return fail(err)
	}
	store, err := users.Open(cfg.UsersFile)
	if err != nil {
		return fail(err)
	}
	pw, err := htpasswd.GeneratePassword(20)
	if err != nil {
		return fail(err)
	}
	if err := store.SetPassword(user, pw); err != nil {
		return fail(err)
	}
	fmt.Printf("New password for %s: %s\nRestart mailpit-crew to end existing sessions.\n", user, pw)
	return 0
}

// hashPassword prints a bcrypt hash for an htpasswd line (user:hash).
func hashPassword() int {
	fmt.Fprint(os.Stderr, "Password: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(os.Stderr, "\nerror: no password given")
		return 1
	}
	h, err := htpasswd.HashPassword(strings.TrimRight(line, "\r\n"))
	if err != nil {
		return fail(err)
	}
	fmt.Println(h)
	return 0
}

// importSetup copies an existing Mailpit setup into Crew's folder.
func importSetup(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mailpit-crew import [flags] </path/to/mailpit.ini>")
		return 2
	}
	src := args[len(args)-1]
	cfg, err := config.Load(args[:len(args)-1], os.Stderr)
	if err != nil {
		return fail(err)
	}
	layout := managed.NewLayout(cfg.MailpitDir(), cfg.DataDir)
	st, err := managed.LoadState(layout.State)
	if err != nil {
		return fail(err)
	}
	if _, err := layout.Ensure(st); err != nil {
		return fail(err)
	}
	fmt.Println("Stop the old Mailpit before importing, so the database copy is complete.")
	notes, err := managed.Import(layout, src)
	for _, n := range notes {
		fmt.Println(" -", n)
	}
	if err != nil {
		return fail(err)
	}
	fmt.Printf("Imported into %s. Start mailpit-crew and choose a Mailpit version.\n", layout.Dir)
	return 0
}

func newLogger(cfg *config.Config) (*slog.Logger, io.Closer, error) {
	var out io.Writer = os.Stdout
	var closer io.Closer = io.NopCloser(nil)
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, nil, err
		}
		out, closer = f, f
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})), closer, nil
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// promptVersion asks which Mailpit to use on the terminal. It returns ""
// when the user skips (choose later in the web UI).
func promptVersion(ctx context.Context, rel *managed.Releases) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	list, err := rel.Latest(ctx, 3)
	fmt.Println("\nNo Mailpit version is selected yet. Which Mailpit should Crew run?")
	if err != nil {
		fmt.Println("  (could not list releases:", err, ")")
	}
	labels := []string{"latest", "latest-1", "latest-2"}
	for i, r := range list {
		fmt.Printf("  %d) %s  (%s, %s)\n", i+1, r.Tag, labels[i], r.Published.Format("2006-01-02"))
	}
	fmt.Println("  4) other: a version tag (v1.28.0) or the path to a Mailpit binary")
	fmt.Println("  Enter) skip, choose later in the web UI")
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Choice: ")
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if err != nil || line == "" {
			return ""
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(list) {
			return list[n-1].Tag
		}
		if line == "4" {
			fmt.Print("Version tag or binary path: ")
			v, _ := in.ReadString('\n')
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
			continue
		}
		if managed.ValidTag(line) || filepath.IsAbs(line) {
			return line
		}
		fmt.Println("Please enter 1-4, or press Enter to skip.")
	}
}

func serve(args []string) error {
	cfg, err := config.Load(args, os.Stderr)
	if err != nil {
		return err
	}
	log, closer, err := newLogger(cfg)
	if err != nil {
		return err
	}
	defer closer.Close()

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	store, err := users.Open(cfg.UsersFile)
	if err != nil {
		return err
	}
	if pw, err := store.Bootstrap(); err != nil {
		return err
	} else if pw != "" {
		// shown once; the hash is stored in the users file
		log.Warn("created the Crew admin login, log in and change the password", "user", users.AdminName, "password", pw, "file", cfg.UsersFile)
		if isTerminal(os.Stdout) {
			fmt.Printf("\nCrew login: %s / %s\n\n", users.AdminName, pw)
		}
	}

	layout := managed.NewLayout(cfg.MailpitDir(), cfg.DataDir)
	st, err := managed.LoadState(layout.State)
	if err != nil {
		return err
	}
	notes, err := layout.Ensure(st)
	if err != nil {
		return fmt.Errorf("prepare %s: %w", layout.Dir, err)
	}
	for _, n := range notes {
		log.Info(n)
	}

	client := mailpit.New(managed.APIBase(nil), st.APIUser, st.APIPassword)
	script := &restart.Script{Path: layout.Ctl, LogFile: layout.Log, Log: log}
	var mgr *apply.Manager
	// Crew talks to the API through Mailpit's web UI address, which can
	// change with the settings. It follows the settings Mailpit runs with.
	followUI := func() {
		if env, err := mgr.Env(); err == nil {
			client.SetBase(managed.APIBase(env.Map()))
		}
	}
	crewPort := cfg.ListenPort()
	mgr = apply.New(apply.Options{
		EnvFile:     layout.EnvFile,
		ManagedDir:  layout.Dir,
		BackupDir:   layout.Backups,
		ActiveDir:   layout.Active,
		Keep:        cfg.Backups.Keep,
		Restarter:   script,
		Ready:       readiness{client, followUI},
		Timeout:     cfg.Restart.Timeout.Duration,
		ServiceUser: st.APIUser,
		Locked:      layout.Locked(),
		Extra:       func(env map[string]string) []catalog.FieldError { return portClashes(env, crewPort) },
		Started:     followUI,
		Log:         log,
	})
	if changed, err := mgr.RepairLocked(); err != nil {
		return err
	} else if len(changed) > 0 {
		log.Warn("reset settings managed by Crew", "keys", strings.Join(changed, ","))
	}
	followUI()
	svc := &managed.Service{
		Layout: layout,
		Releases: &managed.Releases{
			APIURL:      cfg.Managed.ReleasesURL,
			FeedURL:     "https://github.com/axllent/mailpit/releases.atom",
			DownloadURL: cfg.Managed.DownloadURL,
		},
		Script:  script,
		Manager: mgr,
		Log:     log,
	}
	return run(cfg, log, store, mgr, client, svc)
}

// readiness points the client at the new UI address before waiting, since
// a restart may have changed it.
type readiness struct {
	c      *mailpit.Client
	before func()
}

func (r readiness) WaitReady(ctx context.Context) error {
	r.before()
	return r.c.WaitReady(ctx)
}

func (r readiness) Uptime(ctx context.Context) (time.Duration, error) { return r.c.Uptime(ctx) }

// portClashes reports Mailpit ports that are used twice, or by Crew.
func portClashes(env map[string]string, crewPort string) []catalog.FieldError {
	var out []catalog.FieldError
	defaults := map[string]string{"MP_UI_BIND_ADDR": managed.DefaultUIAddr, "MP_SMTP_BIND_ADDR": "[::]:1025", "MP_POP3_BIND_ADDR": "[::]:1110"}
	used := map[string]string{}
	for _, k := range []string{"MP_UI_BIND_ADDR", "MP_SMTP_BIND_ADDR", "MP_POP3_BIND_ADDR"} {
		v := env[k]
		if v == "" {
			if k == "MP_POP3_BIND_ADDR" && env["MP_POP3_AUTH_FILE"] == "" && env["MP_POP3_AUTH"] == "" {
				continue // POP3 is off
			}
			v = defaults[k]
		}
		_, port, err := net.SplitHostPort(v)
		if err != nil {
			continue
		}
		if port == crewPort {
			out = append(out, catalog.FieldError{Key: k, Err: "port " + port + " is used by MailPit-Crew itself"})
		} else if other, ok := used[port]; ok {
			out = append(out, catalog.FieldError{Key: k, Err: "port " + port + " is also used by " + other})
		}
		used[port] = k
	}
	return out
}

func run(cfg *config.Config, log *slog.Logger, store *users.Store, mgr *apply.Manager, client *mailpit.Client, svc *managed.Service) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	choice := cfg.Managed.Version
	if !svc.HasBinary() && choice == "" && isTerminal(os.Stdin) {
		choice = promptVersion(ctx, svc.Releases)
	}
	switch {
	case !svc.HasBinary() && choice != "":
		log.Info("installing Mailpit", "choice", choice)
		name, err := svc.Use(ctx, choice)
		if err != nil {
			log.Error("could not start Mailpit, choose a version in the web UI", "error", err)
		} else {
			log.Info("mailpit running", "binary", name)
		}
	case svc.HasBinary():
		if err := svc.Start(); err != nil {
			log.Error("mailpit did not start", "error", err)
		} else if !mgr.HasActive() {
			// first start with this Crew version: what runs now is the baseline
			if err := mgr.MarkStarted(); err != nil {
				log.Error("record active files", "error", err)
			}
		}
	default:
		log.Warn("no Mailpit version selected yet, choose one in the web UI (Mailpit page)")
	}
	go svc.Watch(ctx, 30*time.Second)

	srv, err := web.New(web.Options{
		Webroot:      cfg.Webroot,
		SecureCookie: cfg.TLS.Cert != "",
		SessionTTL:   cfg.Session.TTL.Duration,
		Version:      version,
		CrewPort:     cfg.ListenPort(),
		Users:        store,
		Manager:      mgr,
		Mailpit:      client,
		Managed:      svc,
		Audit:        audit.New(filepath.Join(cfg.DataDir, "audit.log")),
		Log:          log,
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      15 * time.Minute, // version downloads and restarts run inside requests
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("mailpit-crew started", "version", version, "listen", cfg.Listen, "tls", cfg.TLS.Cert != "", "home", cfg.Home, "config", cfg.ConfigFile)
		if cfg.TLS.Cert != "" {
			errc <- httpSrv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key)
		} else {
			errc <- httpSrv.ListenAndServe()
		}
	}()

	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down; Mailpit keeps running (mailpit/ctl.sh stop to stop it)")
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
	return serveErr
}
