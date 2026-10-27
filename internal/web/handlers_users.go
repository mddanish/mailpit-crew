package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"github.com/mddanish/mailpit-crew/internal/inbox"
	"github.com/mddanish/mailpit-crew/internal/users"
)

type usersData struct {
	Users []users.User
}

func (s *Server) usersPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.render(w, c, "users", "Users", "users", usersData{Users: s.opts.Users.List()})
}

func (s *Server) userAdd(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name := strings.TrimSpace(r.PostFormValue("username"))
	role := r.PostFormValue("role")
	pw := r.PostFormValue("password")
	generated := ""
	if pw == "" {
		var err error
		if pw, err = htpasswd.GeneratePassword(20); err != nil {
			s.flash(c, "error", err.Error())
			s.redirect(w, r, "admin/users")
			return
		}
		generated = pw
	} else if len(pw) < 10 {
		s.flash(c, "error", "Password must have at least 10 characters.")
		s.redirect(w, r, "admin/users")
		return
	}
	if err := s.opts.Users.Add(name, pw, role); err != nil {
		s.flash(c, "error", err.Error())
		s.redirect(w, r, "admin/users")
		return
	}
	s.audit(r, c.user.Username, "user-add", name, "ok", "role "+role)
	msg := "Added " + name + "."
	if role == users.RoleUser {
		msg += " Add rules so the user can see mail."
	}
	if generated != "" {
		s.sessions.addFlash(c.sess, flash{Kind: "ok", Text: msg + " Password (shown only once):", Secret: generated})
	} else {
		s.flash(c, "ok", msg)
	}
	s.redirect(w, r, "admin/users/"+name)
}

type ruleRow struct {
	Index int
	Type  string
	Value string
}

type userData struct {
	U         users.User
	Rules     []ruleRow
	Types     any
	Errors    []string
	Self      bool
	Preview   *inbox.Result
	PreviewOK bool
	PreviewEr string
}

const newRuleRows = 3

func (s *Server) userPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	u, ok := s.opts.Users.Get(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.renderUser(w, r, c, u, nil, nil)
}

func (s *Server) renderUser(w http.ResponseWriter, r *http.Request, c *reqCtx, u users.User, rows []ruleRow, errs []string) {
	if rows == nil {
		for i, rl := range u.Rules {
			rows = append(rows, ruleRow{Index: i, Type: rl.Type, Value: rl.Value})
		}
		for i := 0; i < newRuleRows; i++ {
			rows = append(rows, ruleRow{Index: len(u.Rules) + i, Type: inbox.RuleSMTPUser})
		}
	}
	d := userData{U: u, Rules: rows, Types: inbox.RuleTypes, Errors: errs, Self: u.Username == c.user.Username}
	ctx, cancel := s.ctx(r, 20*time.Second)
	defer cancel()
	if res, err := s.opts.Inbox.List(ctx, u, "", "", 1, 10); err != nil {
		d.PreviewEr = err.Error()
	} else {
		d.Preview, d.PreviewOK = res, true
	}
	if len(errs) > 0 {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, c, "user", u.Username, "users", d)
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name := r.PathValue("name")
	u, ok := s.opts.Users.Get(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	n, _ := strconv.Atoi(r.PostFormValue("rows"))
	if n < 0 || n > 1000 {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var rules []users.Rule
	var rows []ruleRow
	var errs []string
	for i := 0; i < n; i++ {
		k := strconv.Itoa(i)
		rl := users.Rule{Type: r.PostFormValue("type_" + k), Value: strings.TrimSpace(r.PostFormValue("value_" + k))}
		if r.PostForm.Has("del_"+k) || rl.Value == "" {
			continue
		}
		if err := inbox.ValidateRule(rl); err != nil {
			errs = append(errs, err.Error())
		}
		rules = append(rules, rl)
		rows = append(rows, ruleRow{Index: len(rows), Type: rl.Type, Value: rl.Value})
	}
	role := r.PostFormValue("role")
	enabled := r.PostForm.Has("enabled")
	if name == c.user.Username && (role != users.RoleAdmin || !enabled) {
		errs = append(errs, "You cannot remove your own admin role or disable yourself.")
	}
	if len(errs) > 0 {
		for i := 0; i < newRuleRows; i++ {
			rows = append(rows, ruleRow{Index: len(rows), Type: inbox.RuleSMTPUser})
		}
		u.Role, u.Enabled = role, &enabled
		s.renderUser(w, r, c, u, rows, errs)
		return
	}
	err := s.opts.Users.Update(name, func(u *users.User) error {
		u.Role = role
		if enabled {
			u.Enabled = nil
		} else {
			f := false
			u.Enabled = &f
		}
		u.Rules = rules
		return nil
	})
	if err != nil {
		s.flash(c, "error", err.Error())
	} else {
		if !enabled {
			s.sessions.deleteUser(name, "")
		}
		s.audit(r, c.user.Username, "user-update", name, "ok", "role "+role+", rules "+strconv.Itoa(len(rules)))
		s.flash(c, "ok", "Saved "+name+".")
	}
	s.redirect(w, r, "admin/users/"+name)
}

func (s *Server) userPassword(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name := r.PathValue("name")
	if _, ok := s.opts.Users.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	pw, err := htpasswd.GeneratePassword(20)
	if err == nil {
		err = s.opts.Users.SetPassword(name, pw)
	}
	if err != nil {
		s.flash(c, "error", err.Error())
	} else {
		s.sessions.deleteUser(name, c.sess.ID)
		s.audit(r, c.user.Username, "user-password", name, "ok", "")
		s.sessions.addFlash(c.sess, flash{Kind: "ok", Text: "New password for " + name + " (shown only once):", Secret: pw})
	}
	s.redirect(w, r, "admin/users/"+name)
}

func (s *Server) userDelete(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name := r.PathValue("name")
	if name == c.user.Username {
		s.flash(c, "error", "You cannot delete yourself.")
		s.redirect(w, r, "admin/users/"+name)
		return
	}
	if err := s.opts.Users.Delete(name); err != nil {
		s.flash(c, "error", err.Error())
		s.redirect(w, r, "admin/users")
		return
	}
	s.sessions.deleteUser(name, "")
	s.audit(r, c.user.Username, "user-delete", name, "ok", "")
	s.flash(c, "ok", "Deleted "+name+".")
	s.redirect(w, r, "admin/users")
}
