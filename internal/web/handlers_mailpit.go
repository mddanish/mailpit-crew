package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/mailpit"
	"github.com/mddanish/mailpit-crew/internal/managed"
)

type releaseRow struct {
	managed.Release
	Label     string
	Installed bool
	Active    bool
}

type mailpitData struct {
	Active     string
	Running    bool
	Info       *mailpit.Info
	Releases   []releaseRow
	ReleaseErr string
	Installed  []managed.Installed
	Layout     managed.Layout
	LastError  string
	LogTail    []string
	Asset      string
	Warning    string
}

func (s *Server) mailpitPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	svc := s.opts.Managed
	if svc == nil {
		s.render(w, c, "message", "Mailpit", "mailpit", "Crew runs in external mode: Mailpit is installed and run separately.")
		return
	}
	d := mailpitData{
		Active:    svc.ActiveVersion(),
		Running:   svc.Running(),
		Installed: svc.Installed(),
		Layout:    svc.Layout,
		LastError: svc.LastError(),
		LogTail:   s.opts.Manager.LogTail(),
		Asset:     managed.AssetName(),
	}
	d.Warning = usernameWarning(d.Active)
	ctx, cancel := s.ctx(r, 5*time.Second)
	defer cancel()
	if d.Running {
		d.Info, _ = s.opts.Mailpit.Info(ctx)
	}
	installed := map[string]bool{}
	for _, i := range d.Installed {
		installed[i.Version] = true
	}
	rctx, rcancel := s.ctx(r, 10*time.Second)
	defer rcancel()
	list, err := svc.Releases.Latest(rctx, 3)
	if err != nil {
		d.ReleaseErr = err.Error()
	}
	labels := []string{"latest", "latest-1", "latest-2"}
	for i, rel := range list {
		d.Releases = append(d.Releases, releaseRow{Release: rel, Label: labels[i], Installed: installed[rel.Tag], Active: rel.Tag == d.Active})
	}
	s.render(w, c, "mailpit", "Mailpit", "mailpit", d)
}

func (s *Server) mailpitUse(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	svc := s.opts.Managed
	if svc == nil {
		http.NotFound(w, r)
		return
	}
	choice := strings.TrimSpace(r.PostFormValue("choice"))
	// not tied to the request: downloads and restarts must finish
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	name, err := svc.Use(ctx, choice)
	if err != nil {
		s.audit(r, c.user.Username, "mailpit-version", choice, "failed", err.Error())
		s.sessions.addFlash(c.sess, flash{Kind: "error", Text: "Could not switch Mailpit: " + err.Error(), List: s.opts.Manager.LogTail()})
	} else {
		s.audit(r, c.user.Username, "mailpit-version", choice, "ok", name)
		s.flash(c, "ok", "Mailpit is running "+strings.TrimPrefix(name, "mailpit-")+".")
	}
	s.redirect(w, r, "admin/mailpit")
}

func (s *Server) mailpitRemove(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	svc := s.opts.Managed
	if svc == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PostFormValue("name")
	if err := svc.Remove(name); err != nil {
		s.flash(c, "error", err.Error())
	} else {
		s.audit(r, c.user.Username, "mailpit-remove", name, "ok", "")
		s.flash(c, "ok", "Removed "+name+".")
	}
	s.redirect(w, r, "admin/mailpit")
}

// optionalAuthKeys are password files that can be turned on in managed mode.
var optionalAuthKeys = []string{"MP_SMTP_AUTH_FILE", "MP_POP3_AUTH_FILE", "MP_SEND_API_AUTH_FILE"}

type enableOption struct {
	Key   string
	Label string
	Path  string
	Note  string
}

func (s *Server) enableOptions() []enableOption {
	svc := s.opts.Managed
	if svc == nil {
		return nil
	}
	env, err := s.opts.Manager.Env()
	if err != nil {
		return nil
	}
	notes := map[string]string{
		"MP_SMTP_AUTH_FILE":     "Apps must log in to send mail. Without it, Mailpit accepts mail from anyone.",
		"MP_POP3_AUTH_FILE":     "Turns on the POP3 server. Every POP3 account can read all mail.",
		"MP_SEND_API_AUTH_FILE": "Protects the HTTP Send API.",
	}
	var out []enableOption
	for _, k := range optionalAuthKeys {
		if v, _ := env.Get(k); v != "" {
			continue
		}
		key, _ := catalog.Lookup(k)
		out = append(out, enableOption{Key: k, Label: key.AuthFile, Path: svc.Layout.AuthFile(k), Note: notes[k]})
	}
	return out
}

func (s *Server) accountsEnable(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	svc := s.opts.Managed
	key := r.PostFormValue("key")
	ok := false
	for _, k := range optionalAuthKeys {
		ok = ok || k == key
	}
	if svc == nil || !ok {
		http.NotFound(w, r)
		return
	}
	f, found := s.opts.Manager.FileByID("env")
	env, err := s.opts.Manager.Env()
	if !found || err != nil {
		s.flash(c, "error", "Cannot read the Mailpit settings.")
		s.redirect(w, r, "admin/accounts")
		return
	}
	if err := env.Set(key, svc.Layout.AuthFile(key)); err == nil {
		err = s.opts.Manager.Stage(f, env.Bytes())
	}
	if err != nil {
		s.flash(c, "error", err.Error())
	} else {
		s.audit(r, c.user.Username, "stage", f.Path, "ok", "enable "+key)
		s.flash(c, "ok", "Staged "+key+". Add the first account below, then apply on the Changes page.")
	}
	for _, af := range s.opts.Manager.Files() {
		if af.Kind == apply.KindAuth && af.Path == svc.Layout.AuthFile(key) {
			s.redirect(w, r, "admin/accounts/"+af.ID)
			return
		}
	}
	s.redirect(w, r, "admin/accounts")
}
