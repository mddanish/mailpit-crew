package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/envfile"
	"github.com/mddanish/mailpit-crew/internal/mailpit"
)

type check struct {
	Level string // ok, warn, error
	Title string
	Text  string
}

type dashboardData struct {
	Info      *mailpit.Info
	InfoError string
	UIURL     string
	Files     []apply.File
	Checks    []check
	Problems  []string
}

// uiURL is the address users open to read mail in Mailpit.
func uiURL(env map[string]string, requestHost string) string {
	addr := env["MP_UI_BIND_ADDR"]
	if addr == "" {
		addr = "[::]:8025"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	switch host {
	case "", "::", "0.0.0.0":
		host = requestHost
		if h, _, err := net.SplitHostPort(requestHost); err == nil {
			host = h
		}
	}
	scheme := "http"
	if env["MP_UI_TLS_CERT"] != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(strings.Trim(host, "[]"), port) + "/"
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	d := dashboardData{Files: s.opts.Manager.Files()}
	ctx, cancel := s.ctx(r, 5*time.Second)
	defer cancel()
	if info, err := s.opts.Mailpit.Info(ctx); err != nil {
		d.InfoError = err.Error()
	} else {
		d.Info = info
	}
	if env, err := s.opts.Manager.Env(); err == nil {
		d.UIURL = uiURL(env.Map(), r.Host)
		d.Checks = s.checks(env)
	}
	for _, i := range s.opts.Manager.Validate() {
		d.Problems = append(d.Problems, i.String())
	}
	s.render(w, c, "dashboard", "Status", "status", d)
}

// checks points out risky or missing Mailpit settings.
func (s *Server) checks(env *envfile.File) []check {
	m := env.Map()
	var out []check
	if m["MP_SMTP_AUTH_FILE"] == "" && !catalog.IsTrue(m["MP_SMTP_AUTH_ACCEPT_ANY"]) {
		out = append(out, check{"warn", "SMTP accepts mail without login", "Anyone who can reach the SMTP port can send mail. Turn on SMTP accounts on the Accounts page."})
	} else {
		out = append(out, check{"ok", "SMTP login", "Apps must log in to send mail."})
	}
	if m["MP_UI_TLS_CERT"] == "" {
		out = append(out, check{"warn", "Mailpit web UI without HTTPS", "Logins and mail are sent unencrypted. Set MP_UI_TLS_CERT and MP_UI_TLS_KEY in Settings."})
	} else {
		out = append(out, check{"ok", "Mailpit web UI uses HTTPS", m["MP_UI_TLS_CERT"]})
	}
	if pop := m["MP_POP3_AUTH_FILE"]; pop != "" && filepath.Clean(pop) == filepath.Clean(m["MP_UI_AUTH_FILE"]) {
		out = append(out, check{"warn", "POP3 uses the web UI password file", "Every web UI account, including the Crew service account, can read all mail over POP3."})
	}
	return out
}

type pendingData struct {
	Changes  []apply.Change
	Problems []string
}

func (s *Server) pendingPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	d := pendingData{Changes: s.opts.Manager.Pending()}
	for _, i := range s.opts.Manager.Validate() {
		d.Problems = append(d.Problems, i.String())
	}
	s.render(w, c, "pending", "Changes not active", "pending", d)
}

func (s *Server) restartSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	// not tied to the request: a closed tab must not stop a restart halfway
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err := s.opts.Manager.Restart(ctx)
	var ve *apply.ValidationError
	switch {
	case errors.As(err, &ve):
		var list []string
		for _, i := range ve.Issues {
			list = append(list, i.String())
		}
		s.sessions.addFlash(c.sess, flash{Kind: "error", Text: "Mailpit was not restarted. Fix these problems first:", List: list})
		s.audit(r, c.user.Username, "restart", "", "refused", err.Error())
	case err != nil:
		// the failure banner explains the choices: fix or roll back
		s.audit(r, c.user.Username, "restart", "", "failed", err.Error())
	default:
		s.audit(r, c.user.Username, "restart", "", "ok", "")
		s.flash(c, "ok", "Mailpit restarted. All saved changes are active.")
	}
	s.redirect(w, r, back(r, "admin"))
}

func (s *Server) rollbackSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	restored, err := s.opts.Manager.Rollback(ctx)
	if err != nil {
		s.audit(r, c.user.Username, "rollback", strings.Join(restored, ","), "failed", err.Error())
		s.flash(c, "error", "Rollback did not bring Mailpit back: "+err.Error())
	} else {
		s.audit(r, c.user.Username, "rollback", strings.Join(restored, ","), "ok", "")
		s.sessions.addFlash(c.sess, flash{Kind: "ok", Text: "Restored the last working files and restarted Mailpit.", List: restored})
	}
	s.redirect(w, r, "admin")
}

// back returns a safe page to go back to after a POST.
func back(r *http.Request, fallback string) string {
	b := strings.TrimPrefix(r.PostFormValue("back"), "/")
	if strings.HasPrefix(b, "admin") && !strings.Contains(b, "//") && !strings.Contains(b, "\\") {
		return b
	}
	return fallback
}
