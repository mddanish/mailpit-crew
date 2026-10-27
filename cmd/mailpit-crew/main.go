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
	"strings"
	"syscall"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/audit"
	"github.com/mddanish/mailpit-crew/internal/config"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"github.com/mddanish/mailpit-crew/internal/mailpit"
	"github.com/mddanish/mailpit-crew/internal/restart"
	"github.com/mddanish/mailpit-crew/internal/users"
	"github.com/mddanish/mailpit-crew/internal/web"
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "dev"

const usage = `MailPit-Crew %s - admin panel for Mailpit (unofficial)

Usage:
  mailpit-crew [flags]                         run the server
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

func resetPassword(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mailpit-crew reset-password [flags] <user>")
		return 2
	}
	user := args[len(args)-1]
	cfg, err := config.Load(args[:len(args)-1], os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	store, err := users.Open(cfg.UsersFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	pw, err := htpasswd.GeneratePassword(20)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := store.SetPassword(user, pw); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
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
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println(h)
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
	}

	client := mailpit.New(cfg.Mailpit.APIURL, cfg.Mailpit.APIUser, cfg.Mailpit.APIPassword)

	var rs restart.Restarter
	switch cfg.Restart.Mode {
	case "child":
		rs = &restart.Child{Binary: cfg.Child.Binary, Args: cfg.Child.Args, EnvFile: cfg.Mailpit.EnvFile, Log: log.With("component", "child")}
	default:
		rs = &restart.Command{Args: cfg.Restart.Command, Log: log}
	}

	mgr := apply.New(apply.Options{
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
		Restarter:   rs,
		Ready:       client,
		Timeout:     cfg.Restart.Timeout.Duration,
		ServiceUser: cfg.Mailpit.APIUser,
		Log:         log,
	})

	srv, err := web.New(web.Options{
		Webroot:      cfg.Webroot,
		SecureCookie: cfg.TLS.Cert != "",
		SessionTTL:   cfg.Session.TTL.Duration,
		ApplyTimeout: cfg.Restart.Timeout.Duration,
		Version:      version,
		RestartMode:  cfg.Restart.Mode,
		Users:        store,
		Manager:      mgr,
		Mailpit:      client,
		Audit:        audit.New(filepath.Join(cfg.DataDir, "audit.log")),
		Log:          log,
	})
	if err != nil {
		return err
	}

	if err := rs.Start(); err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      3*cfg.Restart.Timeout.Duration + 30*time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("mailpit-crew started", "version", version, "listen", cfg.Listen, "tls", cfg.TLS.Cert != "", "restart_mode", cfg.Restart.Mode, "config", cfg.ConfigFile)
		if cfg.TLS.Cert != "" {
			errc <- httpSrv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key)
		} else {
			errc <- httpSrv.ListenAndServe()
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s.String())
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = rs.Stop(ctx)
			cancel()
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	return rs.Stop(ctx)
}
