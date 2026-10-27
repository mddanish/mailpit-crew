// Command mailpit-crew is an admin panel for Mailpit.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

const usage = `MailPit-Crew %s - runs and manages Mailpit (unofficial)

Usage:
  mailpit-crew [flags]                         run Crew (and Mailpit, in managed mode)
  mailpit-crew import [flags] <mailpit.ini>    import an existing Mailpit setup (managed mode)
  mailpit-crew reset-password [flags] <user>   set a new random password for a Crew user
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
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mailpit-crew reset-password [flags] <user>")
		return 2
	}
	user := args[len(args)-1]
	cfg, err := config.Load(args[:len(args)-1], os.Stderr)
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

// importSetup copies an existing Mailpit setup into the managed layout.
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
	if cfg.Mode != "managed" {
		return fail(errors.New("import needs mode managed"))
	}
	layout := managed.NewLayout(cfg.MailpitDir(), cfg.DataDir)
	st, err := managed.LoadState(layout.State)
	if err != nil {
		return fail(err)
	}
	if _, err := layout.Ensure(cfg.Managed.APIListen, st); err != nil {
		return fail(err)
	}
	store, err := users.Open(cfg.UsersFile)
	if err != nil {
		return fail(err)
	}
	fmt.Println("Stop the old Mailpit before importing, so the database copy is complete.")
	notes, err := managed.Import(layout, src, store)
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
		log.Warn("created first Crew admin, log in and change the password", "user", "admin", "password", pw, "file", cfg.UsersFile)
		if isTerminal(os.Stdout) {
			fmt.Printf("\nFirst Crew login: admin / %s\n\n", pw)
		}
	}

	opts := apply.Options{
		EnvFile: cfg.Mailpit.EnvFile,
		Overrides: map[string]string{
			"MP_UI_AUTH_FILE":       cfg.Mailpit.UIAuthFile,
			"MP_SMTP_AUTH_FILE":     cfg.Mailpit.SMTPAuthFile,
			"MP_POP3_AUTH_FILE":     cfg.Mailpit.POP3AuthFile,
			"MP_SEND_API_AUTH_FILE": cfg.Mailpit.SendAPIAuthFile,
			"MP_TAGS_CONFIG":        cfg.Mailpit.TagsFile,
			"MP_LOG_FILE":           cfg.Mailpit.LogFile,
		},
		ManagedDirs: cfg.Mailpit.ManagedDirs,
		BackupDir:   filepath.Join(cfg.DataDir, "backups"),
		Keep:        cfg.Backups.Keep,
		Timeout:     cfg.Restart.Timeout.Duration,
		ServiceUser: cfg.Mailpit.APIUser,
		Log:         log,
	}

	var (
		client *mailpit.Client
		svc    *managed.Service
		rs     restart.Restarter
	)
	if cfg.Mode == "managed" {
		layout := managed.NewLayout(cfg.MailpitDir(), cfg.DataDir)
		st, err := managed.LoadState(layout.State)
		if err != nil {
			return err
		}
		notes, err := layout.Ensure(cfg.Managed.APIListen, st)
		if err != nil {
			return fmt.Errorf("prepare %s: %w", layout.Dir, err)
		}
		for _, n := range notes {
			log.Info(n)
		}
		client = mailpit.New(cfg.Mailpit.APIURL, st.APIUser, st.APIPassword)
		script := &restart.Script{Path: layout.Ctl, LogFile: layout.Log, Log: log}
		rs = script
		opts.ServiceUser = st.APIUser
		opts.Locked = layout.Locked(cfg.Managed.APIListen)
		opts.Restarter, opts.Ready = rs, client
		mgr := apply.New(opts)
		if changed, err := mgr.RepairLocked(); err != nil {
			return err
		} else if len(changed) > 0 {
			log.Warn("reset settings managed by Crew", "keys", strings.Join(changed, ","))
		}
		svc = &managed.Service{
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
		return run(cfg, log, store, mgr, client, rs, svc)
	}

	client = mailpit.New(cfg.Mailpit.APIURL, cfg.Mailpit.APIUser, cfg.Mailpit.APIPassword)
	switch cfg.Restart.Mode {
	case "child":
		rs = &restart.Child{Binary: cfg.Child.Binary, Args: cfg.Child.Args, EnvFile: cfg.Mailpit.EnvFile, Log: log.With("component", "child")}
	default:
		rs = &restart.Command{Args: cfg.Restart.Command, Log: log}
	}
	opts.Restarter, opts.Ready = rs, client
	return run(cfg, log, store, apply.New(opts), client, rs, nil)
}

func run(cfg *config.Config, log *slog.Logger, store *users.Store, mgr *apply.Manager, client *mailpit.Client, rs restart.Restarter, svc *managed.Service) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if svc != nil {
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
			}
		default:
			log.Warn("no Mailpit version selected yet, choose one in the web UI (Mailpit page)")
		}
		go svc.Watch(ctx, 30*time.Second)
	} else if err := rs.Start(); err != nil {
		return err
	}

	srv, err := web.New(web.Options{
		Webroot:      cfg.Webroot,
		SecureCookie: cfg.TLS.Cert != "",
		SessionTTL:   cfg.Session.TTL.Duration,
		ApplyTimeout: cfg.Restart.Timeout.Duration,
		Version:      version,
		RestartMode:  rs.Mode(),
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
		WriteTimeout:      15 * time.Minute, // version downloads and applies run inside requests
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("mailpit-crew started", "version", version, "mode", cfg.Mode, "listen", cfg.Listen, "tls", cfg.TLS.Cert != "", "home", cfg.Home, "config", cfg.ConfigFile)
		if cfg.TLS.Cert != "" {
			errc <- httpSrv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key)
		} else {
			errc <- httpSrv.ListenAndServe()
		}
	}()

	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
	if svc == nil {
		// external child mode: Mailpit is Crew's child and stops with it.
		// Managed mode leaves Mailpit running; use mailpit/ctl.sh stop.
		_ = rs.Stop(sctx)
	}
	return serveErr
}
