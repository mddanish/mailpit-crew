package web

import (
	"net/http"
	"strings"
)

func (s *Server) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     s.opts.Webroot,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.opts.SecureCookie,
		SameSite: http.SameSiteStrictMode,
	})
}

type loginData struct {
	Username string
	Error    string
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(cookieName); err == nil && s.sessions.get(ck.Value) != nil {
		s.redirect(w, r, "")
		return
	}
	s.render(w, nil, "login", "Log in", "", loginData{})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	ip := clientIP(r)
	keys := []string{"ip:" + ip, "user:" + username}

	fail := func(msg string) {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, nil, "login", "Log in", "", loginData{Username: username, Error: msg})
	}
	if s.limiter.blocked(keys...) {
		s.audit(r, username, "login", "", "blocked", "too many failed attempts")
		fail("Too many failed attempts. Try again later.")
		return
	}
	u, ok := s.opts.Users.Authenticate(username, password)
	if !ok {
		s.limiter.fail(keys...)
		s.audit(r, username, "login", "", "failed", "")
		s.opts.Log.Warn("failed login", "user", username, "ip", ip)
		fail("Wrong username or password.")
		return
	}
	if !u.IsAdmin() {
		s.audit(r, username, "login", "", "denied", "not an admin")
		fail("Only admins can log in to this version of MailPit-Crew.")
		return
	}
	s.limiter.reset(keys...)
	sess := s.sessions.create(u.Username)
	s.setSessionCookie(w, sess.ID, int(s.opts.SessionTTL.Seconds()))
	s.audit(r, u.Username, "login", "", "ok", "")
	s.redirect(w, r, "")
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.sessions.delete(c.sess.ID)
	s.setSessionCookie(w, "", -1)
	s.audit(r, c.user.Username, "logout", "", "ok", "")
	s.redirect(w, r, "login")
}

type passwordData struct {
	Errors []string
}

func (s *Server) passwordPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.render(w, c, "password", "Change password", "password", passwordData{})
}

func (s *Server) passwordSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	current := r.PostFormValue("current")
	next := r.PostFormValue("new")
	confirm := r.PostFormValue("confirm")
	var errs []string
	if _, ok := s.opts.Users.Authenticate(c.user.Username, current); !ok {
		errs = append(errs, "Current password is wrong.")
	}
	if len(next) < 10 {
		errs = append(errs, "New password must have at least 10 characters.")
	}
	if len(next) > 72 {
		errs = append(errs, "New password must have 72 characters or less.")
	}
	if next != confirm {
		errs = append(errs, "New passwords do not match.")
	}
	if len(errs) == 0 {
		if err := s.opts.Users.SetPassword(c.user.Username, next); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, c, "password", "Change password", "password", passwordData{Errors: errs})
		return
	}
	s.sessions.deleteUser(c.user.Username, c.sess.ID)
	s.audit(r, c.user.Username, "change-password", c.user.Username, "ok", "")
	s.flash(c, "ok", "Password changed. Other sessions were logged out.")
	s.redirect(w, r, "account/password")
}
