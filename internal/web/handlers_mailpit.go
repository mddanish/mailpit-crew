package web

import (
	"context"
	"net/http"
	"strings"
	"time"

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
}

func (s *Server) mailpitPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	svc := s.opts.Managed
	d := mailpitData{
		Active:    svc.ActiveVersion(),
		Running:   svc.Running(),
		Installed: svc.Installed(),
		Layout:    svc.Layout,
		LastError: svc.LastError(),
		LogTail:   s.opts.Manager.LogTail(),
		Asset:     managed.AssetName(),
	}
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
	name := r.PostFormValue("name")
	if err := svc.Remove(name); err != nil {
		s.flash(c, "error", err.Error())
	} else {
		s.audit(r, c.user.Username, "mailpit-remove", name, "ok", "")
		s.flash(c, "ok", "Removed "+name+".")
	}
	s.redirect(w, r, "admin/mailpit")
}
