package web

import (
	"net/http"
	"strings"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
)

// optionalAuthKeys are password files that can be turned on and off.
var optionalAuthKeys = []string{"MP_SMTP_AUTH_FILE", "MP_POP3_AUTH_FILE", "MP_SEND_API_AUTH_FILE"}

var authNotes = map[string]string{
	"MP_UI_AUTH_FILE":       "Logins for the Mailpit web UI and API, where users read mail.",
	"MP_SMTP_AUTH_FILE":     "Apps must log in to send mail. Without it, Mailpit accepts mail from anyone.",
	"MP_POP3_AUTH_FILE":     "Turns on the POP3 server. Every POP3 account can read all mail.",
	"MP_SEND_API_AUTH_FILE": "Protects the HTTP Send API. Without it, the web UI logins are used.",
}

type authFileRow struct {
	apply.File
	Accounts int
	Note     string
	Optional bool
}

type enableOption struct {
	Key   string
	Label string
	Path  string
	Note  string
}

type accountsData struct {
	Files  []authFileRow
	Enable []enableOption
}

func (s *Server) accountsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	var d accountsData
	set := map[string]bool{}
	for _, f := range s.opts.Manager.Files() {
		if f.Kind != apply.KindAuth {
			continue
		}
		data, _, _ := s.opts.Manager.Read(f.Path)
		n := 0
		for _, e := range htpasswd.Parse(data).Entries() {
			if !s.isService(f, e.User) {
				n++
			}
		}
		row := authFileRow{File: f, Accounts: n}
		for _, k := range f.Uses {
			set[k] = true
			row.Note = authNotes[k]
			row.Optional = row.Optional || k != "MP_UI_AUTH_FILE"
		}
		d.Files = append(d.Files, row)
	}
	for _, k := range optionalAuthKeys {
		if !set[k] {
			key, _ := catalog.Lookup(k)
			d.Enable = append(d.Enable, enableOption{Key: k, Label: key.AuthFile, Path: s.opts.Managed.Layout.StandardFile(k), Note: authNotes[k]})
		}
	}
	s.render(w, c, "accounts", "Accounts", "accounts", d)
}

func isOptional(key string) bool {
	for _, k := range optionalAuthKeys {
		if k == key {
			return true
		}
	}
	return false
}

// accountsEnable creates a password file with its first account and turns
// the setting on.
func (s *Server) accountsEnable(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	key := r.PostFormValue("key")
	if !isOptional(key) {
		http.NotFound(w, r)
		return
	}
	user := strings.TrimSpace(r.PostFormValue("username"))
	pw, generated, err := passwordFrom(r.PostFormValue("password"))
	if err == nil {
		err = htpasswd.ValidUsername(user)
	}
	if err != nil {
		s.flash(c, "error", err.Error())
		s.redirect(w, r, "admin/accounts")
		return
	}
	path := s.opts.Managed.Layout.StandardFile(key)
	data, base, _ := s.opts.Manager.Read(path)
	hf := htpasswd.Parse(data)
	h, err := htpasswd.HashPassword(pw)
	if err == nil {
		err = hf.Set(user, h)
	}
	if err == nil {
		err = s.opts.Manager.Save(apply.KindAuth, path, hf.Bytes(), base)
	}
	if err == nil {
		err = s.setEnv(key, path)
	}
	if err != nil {
		s.flash(c, "error", err.Error())
		s.redirect(w, r, "admin/accounts")
		return
	}
	s.audit(r, c.user.Username, "enable", key, "ok", "first account "+user)
	s.accountFlash(c, "Turned on "+key+" with account "+user+". Restart Mailpit to make it active.", generated)
	s.redirect(w, r, "admin/accounts/"+apply.FileID(apply.KindAuth, path))
}

// passwordFrom returns the given password, or a generated one when empty.
func passwordFrom(pw string) (string, string, error) {
	if pw == "" {
		g, err := htpasswd.GeneratePassword(20)
		return g, g, err
	}
	if len(pw) < 8 {
		return "", "", errPassword
	}
	return pw, "", nil
}

type simpleError string

func (e simpleError) Error() string { return string(e) }

const errPassword = simpleError("password must have at least 8 characters")

func (s *Server) accountFlash(c *reqCtx, msg, generated string) {
	if generated != "" {
		s.sessions.addFlash(c.sess, flash{Kind: "ok", Text: msg + " Generated password (shown only once):", Secret: generated})
		return
	}
	s.flash(c, "ok", msg)
}

type accountRow struct {
	User   string
	Type   string
	Print  string
	SameAs []string
}

type accountFileData struct {
	File     apply.File
	Rows     []accountRow
	Base     string
	Invalid  []int
	Note     string
	Optional string // env key that can be turned off
	Service  bool   // the file holds Crew's hidden service account
}

func (s *Server) isService(f apply.File, user string) bool {
	if user != s.opts.Manager.ServiceUser() {
		return false
	}
	for _, u := range f.Uses {
		if u == "MP_UI_AUTH_FILE" {
			return true
		}
	}
	return false
}

func (s *Server) authFile(w http.ResponseWriter, r *http.Request) (apply.File, *htpasswd.File, string, bool) {
	f, ok := s.opts.Manager.FileByID(r.PathValue("id"))
	if !ok || f.Kind != apply.KindAuth {
		http.NotFound(w, r)
		return f, nil, "", false
	}
	data, base, err := s.opts.Manager.Read(f.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return f, nil, "", false
	}
	return f, htpasswd.Parse(data), base, true
}

func (s *Server) accountFilePage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f, hf, base, ok := s.authFile(w, r)
	if !ok {
		return
	}
	d := accountFileData{File: f, Base: base, Invalid: hf.Invalid()}
	for _, k := range f.Uses {
		d.Note = authNotes[k]
		if isOptional(k) && len(f.Uses) == 1 {
			d.Optional = k
		}
	}
	byHash := map[string][]string{}
	for _, e := range hf.Entries() {
		byHash[e.Hash] = append(byHash[e.Hash], e.User)
	}
	for _, e := range hf.Entries() {
		if s.isService(f, e.User) {
			d.Service = true
			continue
		}
		row := accountRow{User: e.User, Type: htpasswd.HashType(e.Hash), Print: htpasswd.Fingerprint(e.Hash)}
		for _, other := range byHash[e.Hash] {
			if other != e.User {
				row.SameAs = append(row.SameAs, other)
			}
		}
		d.Rows = append(d.Rows, row)
	}
	s.render(w, c, "account_file", f.Label, "accounts", d)
}

func (s *Server) accountAction(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f, hf, _, ok := s.authFile(w, r)
	if !ok {
		return
	}
	page := "admin/accounts/" + f.ID
	action := r.PathValue("action")
	fail := func(msg string) {
		s.flash(c, "error", msg)
		s.redirect(w, r, page)
	}
	if action == "disable" {
		key := r.PostFormValue("key")
		if !isOptional(key) {
			http.NotFound(w, r)
			return
		}
		if err := s.setEnv(key, ""); err != nil {
			fail(err.Error())
			return
		}
		s.audit(r, c.user.Username, "disable", key, "ok", "")
		s.flash(c, "ok", "Turned off "+key+" (the file is kept). Restart Mailpit to make it active.")
		s.redirect(w, r, "admin/accounts")
		return
	}
	user := strings.TrimSpace(r.PostFormValue("username"))
	if err := htpasswd.ValidUsername(user); err != nil {
		fail(err.Error())
		return
	}
	if s.isService(f, user) {
		fail(user + " is reserved for Crew.")
		return
	}
	_, exists := hf.Get(user)
	generated := ""
	switch action {
	case "add", "reset":
		if action == "add" && exists {
			fail("Account " + user + " already exists.")
			return
		}
		if action == "reset" && !exists {
			fail("Account " + user + " not found.")
			return
		}
		pw, g, err := passwordFrom(r.PostFormValue("password"))
		if err != nil {
			fail(err.Error())
			return
		}
		generated = g
		h, err := htpasswd.HashPassword(pw)
		if err == nil {
			err = hf.Set(user, h)
		}
		if err != nil {
			fail(err.Error())
			return
		}
	case "delete":
		if !hf.Delete(user) {
			fail("Account " + user + " not found.")
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	err := s.opts.Manager.Save(apply.KindAuth, f.Path, hf.Bytes(), r.PostFormValue("base"))
	if err != nil {
		res := saveResult{Err: err}
		if ve, ok := err.(*apply.ValidationError); ok {
			res = saveResult{Issues: ve.Issues}
		}
		s.flashSave(c, res, "")
		s.redirect(w, r, page)
		return
	}
	s.audit(r, c.user.Username, "save", f.Path, "ok", "account "+action+": "+user)
	verb := map[string]string{"add": "Added", "reset": "New password for", "delete": "Deleted"}[action]
	s.accountFlash(c, verb+" "+user+". Restart Mailpit to make it active.", generated)
	s.redirect(w, r, page)
}
