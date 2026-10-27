// Package web serves the MailPit-Crew web UI.
package web

import (
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/audit"
	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/mailpit"
	"github.com/mddanish/mailpit-crew/internal/users"
)

//go:embed assets
var assets embed.FS

const cookieName = "mpc_session"

// Options configures the web server.
type Options struct {
	Webroot      string
	SecureCookie bool
	SessionTTL   time.Duration
	ApplyTimeout time.Duration
	Version      string
	RestartMode  string
	Users        *users.Store
	Manager      *apply.Manager
	Mailpit      *mailpit.Client
	Audit        *audit.Log
	Log          *slog.Logger
}

// Server is the web UI.
type Server struct {
	opts     Options
	sessions *sessionStore
	limiter  *limiter
	pages    map[string]*template.Template
	mux      *http.ServeMux
}

// New builds the server and parses templates.
func New(opts Options) (*Server, error) {
	s := &Server{
		opts:     opts,
		sessions: newSessionStore(opts.SessionTTL),
		limiter:  newLimiter(),
		pages:    map[string]*template.Template{},
		mux:      http.NewServeMux(),
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

var funcs = template.FuncMap{
	"join": strings.Join,
	"inc":  func(i int) int { return i + 1 },
	"uptime": func(sec uint64) string {
		return (time.Duration(sec) * time.Second).String()
	},
	"bytes":  humanBytes,
	"istrue": catalog.IsTrue,
}

func (s *Server) parseTemplates() error {
	names, err := fs.Glob(assets, "assets/templates/*.html")
	if err != nil {
		return err
	}
	for _, n := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(n, "assets/templates/"), ".html")
		if base == "layout" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, "assets/templates/layout.html", n)
		if err != nil {
			return err
		}
		s.pages[base] = t
	}
	return nil
}

func (s *Server) routes() {
	static, _ := fs.Sub(assets, "assets/static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	s.mux.HandleFunc("GET /login", s.loginPage)
	s.mux.HandleFunc("POST /login", s.loginSubmit)
	s.mux.HandleFunc("POST /logout", s.auth(s.logout))
	s.mux.HandleFunc("GET /{$}", s.auth(func(w http.ResponseWriter, r *http.Request, c *reqCtx) {
		s.redirect(w, r, "admin")
	}))

	s.mux.HandleFunc("GET /account/password", s.auth(s.passwordPage))
	s.mux.HandleFunc("POST /account/password", s.auth(s.passwordSubmit))

	s.mux.HandleFunc("GET /admin", s.admin(s.dashboard))
	s.mux.HandleFunc("GET /admin/settings", s.admin(s.settingsPage))
	s.mux.HandleFunc("POST /admin/settings", s.admin(s.settingsSubmit))
	s.mux.HandleFunc("GET /admin/settings/raw", s.admin(s.rawPage))
	s.mux.HandleFunc("POST /admin/settings/raw", s.admin(s.rawSubmit))
	s.mux.HandleFunc("GET /admin/accounts", s.admin(s.accountsPage))
	s.mux.HandleFunc("GET /admin/accounts/{id}", s.admin(s.accountFilePage))
	s.mux.HandleFunc("POST /admin/accounts/{id}/{action}", s.admin(s.accountAction))
	s.mux.HandleFunc("GET /admin/tags", s.admin(s.tagsPage))
	s.mux.HandleFunc("POST /admin/tags", s.admin(s.tagsSubmit))
	s.mux.HandleFunc("GET /admin/tags/test", s.admin(s.tagsTest))
	s.mux.HandleFunc("GET /admin/changes", s.admin(s.changesPage))
	s.mux.HandleFunc("POST /admin/changes/discard", s.admin(s.discard))
	s.mux.HandleFunc("POST /admin/apply", s.admin(s.applySubmit))
}

// Handler returns the HTTP handler, mounted at the webroot.
func (s *Server) Handler() http.Handler {
	h := http.Handler(s.mux)
	if root := strings.TrimSuffix(s.opts.Webroot, "/"); root != "" {
		h = http.StripPrefix(root, h)
	}
	return securityHeaders(h)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// reqCtx is per-request state for logged-in users.
type reqCtx struct {
	sess *session
	user users.User
}

type handler func(w http.ResponseWriter, r *http.Request, c *reqCtx)

func (s *Server) path(p string) string { return s.opts.Webroot + strings.TrimPrefix(p, "/") }

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, p string) {
	http.Redirect(w, r, s.path(p), http.StatusSeeOther)
}

// sameOrigin rejects cross-site form posts (extra layer next to CSRF tokens).
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return o == "" // no Origin header: older clients; CSRF token still applies
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func (s *Server) auth(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sess *session
		if ck, err := r.Cookie(cookieName); err == nil {
			sess = s.sessions.get(ck.Value)
		}
		if sess == nil {
			s.redirect(w, r, "login")
			return
		}
		u, ok := s.opts.Users.Get(sess.User)
		if !ok || !u.IsEnabled() {
			s.sessions.delete(sess.ID)
			s.redirect(w, r, "login")
			return
		}
		if r.Method == http.MethodPost {
			if !sameOrigin(r) || !tokensEqual(r.PostFormValue("csrf"), sess.CSRF) {
				http.Error(w, "invalid or missing CSRF token, reload the page and try again", http.StatusForbidden)
				return
			}
		}
		h(w, r, &reqCtx{sess: sess, user: u})
	}
}

func (s *Server) admin(h handler) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request, c *reqCtx) {
		if !c.user.IsAdmin() {
			http.Error(w, "admin role required", http.StatusForbidden)
			return
		}
		h(w, r, c)
	})
}

type pageData struct {
	Title   string
	Nav     string
	Root    string
	Version string
	User    *users.User
	CSRF    string
	Flashes []flash
	Pending int
	Data    any
}

func (s *Server) render(w http.ResponseWriter, c *reqCtx, name, title, nav string, data any) {
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	pd := pageData{Title: title, Nav: nav, Root: s.opts.Webroot, Version: s.opts.Version, Data: data}
	if c != nil {
		u := c.user
		pd.User = &u
		pd.CSRF = c.sess.CSRF
		pd.Flashes = s.sessions.takeFlashes(c.sess)
		if u.IsAdmin() {
			pd.Pending = s.opts.Manager.PendingCount()
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, pd); err != nil {
		s.opts.Log.Error("render", "page", name, "error", err)
	}
}

func (s *Server) flash(c *reqCtx, kind, text string) {
	s.sessions.addFlash(c.sess, flash{Kind: kind, Text: text})
}

func (s *Server) audit(r *http.Request, user, action, target, result, detail string) {
	if s.opts.Audit == nil {
		return
	}
	if err := s.opts.Audit.Write(audit.Entry{User: user, IP: clientIP(r), Action: action, Target: target, Result: result, Detail: detail}); err != nil {
		s.opts.Log.Error("audit log", "error", err)
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) ctx(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
