package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/envfile"
	"github.com/mddanish/mailpit-crew/internal/htpasswd"
	"github.com/mddanish/mailpit-crew/internal/mailpit"
)

type check struct {
	Level string // ok, warn, error
	Title string
	Text  string
}

type dashboardData struct {
	Info        *mailpit.Info
	InfoError   string
	RestartMode string
	EnvFile     string
	Files       []apply.File
	Checks      []check
	Last        *apply.Result
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	d := dashboardData{RestartMode: s.opts.RestartMode, Files: s.opts.Manager.Files(), Last: s.opts.Manager.LastResult()}
	ctx, cancel := s.ctx(r, 5*time.Second)
	defer cancel()
	if info, err := s.opts.Mailpit.Info(ctx); err != nil {
		d.InfoError = err.Error()
	} else {
		d.Info = info
	}
	env, err := s.opts.Manager.DiskEnv()
	switch {
	case errors.Is(err, apply.ErrNoEnvFile):
		d.Checks = append(d.Checks, check{"warn", "No Mailpit env file", "Set mailpit.env_file in the Crew config to manage Mailpit settings."})
	case err != nil:
		d.Checks = append(d.Checks, check{"error", "Cannot read Mailpit env file", err.Error()})
	default:
		d.Checks = s.checks(env)
	}
	s.render(w, c, "dashboard", "Status", "status", d)
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checks reports the required Mailpit settings (spec section 2.1).
func (s *Server) checks(env *envfile.File) []check {
	m := env.Map()
	var out []check
	if m["MP_DATABASE"] == "" {
		out = append(out, check{"error", "MP_DATABASE is not set", "Mailpit keeps mail in a temporary database. Every restart loses all mail."})
	} else {
		out = append(out, check{"ok", "Persistent database", m["MP_DATABASE"]})
	}
	if addr := m["MP_UI_BIND_ADDR"]; isLoopback(addr) {
		out = append(out, check{"ok", "Mailpit UI is local only", addr})
	} else {
		if addr == "" {
			addr = "[::]:8025 (default)"
		}
		out = append(out, check{"warn", "Mailpit UI is reachable from the network", addr + ". Users can bypass Crew. Set MP_UI_BIND_ADDR to 127.0.0.1:8026."})
	}
	uiAuth := m["MP_UI_AUTH_FILE"]
	switch {
	case uiAuth == "":
		out = append(out, check{"warn", "Mailpit API has no authentication", "Set MP_UI_AUTH_FILE and add the Crew service account."})
	case s.opts.Manager.ServiceUser() == "":
		out = append(out, check{"warn", "No Crew service account", "Mailpit API needs a login. Set mailpit.api_user and mailpit.api_password in the Crew config."})
	default:
		out = append(out, check{"ok", "Mailpit API authentication", "Crew uses service account " + s.opts.Manager.ServiceUser() + "."})
	}
	if catalog.IsTrue(m["MP_TAGS_USERNAME"]) {
		out = append(out, check{"ok", "Username tags enabled", "Messages are tagged with the SMTP username."})
	} else {
		out = append(out, check{"warn", "MP_TAGS_USERNAME is off", "Needed for per-user inboxes (phase 2)."})
	}
	if pop := m["MP_POP3_AUTH_FILE"]; pop != "" && uiAuth != "" && filepath.Clean(pop) == filepath.Clean(uiAuth) {
		out = append(out, check{"warn", "POP3 shares the web UI password file", "Every account in it (including the Crew service account) can read all mail over POP3. Use a separate POP3 file with admin accounts only."})
	}
	return out
}

// settings

type settingField struct {
	Key   catalog.Key
	Value string
	Set   bool
	Error string
}

type settingSection struct {
	Name   string
	Fields []settingField
	HasSet bool
}

type settingsData struct {
	Sections []settingSection
	Other    []settingField
	Errors   []string
	Staged   bool
	Path     string
	Writable bool
}

func (s *Server) envFile(w http.ResponseWriter, c *reqCtx) (apply.File, *envfile.File, bool) {
	f, ok := s.opts.Manager.FileByID("env")
	if !ok {
		s.render(w, c, "message", "Settings", "settings", "No Mailpit env file is configured. Set mailpit.env_file in the Crew config.")
		return f, nil, false
	}
	env, err := s.opts.Manager.Env()
	if err != nil {
		s.render(w, c, "message", "Settings", "settings", "Cannot read the Mailpit env file: "+err.Error())
		return f, nil, false
	}
	return f, env, true
}

func (s *Server) buildSettings(f apply.File, env *envfile.File, values map[string]string, errs map[string]string) settingsData {
	_, staged, _ := s.opts.Manager.Read(f)
	d := settingsData{Staged: staged, Path: f.Path, Writable: f.Writable}
	cur := env.Map()
	for _, sec := range catalog.Sections {
		ss := settingSection{Name: sec}
		for _, k := range catalog.BySection(sec) {
			v, set := cur[k.Name]
			if values != nil && !k.Sensitive {
				v = values[k.Name]
			}
			if k.Sensitive {
				v = ""
			}
			ss.Fields = append(ss.Fields, settingField{Key: k, Value: v, Set: set, Error: errs[k.Name]})
			if set {
				ss.HasSet = true
			}
		}
		d.Sections = append(d.Sections, ss)
	}
	for _, k := range env.Keys() {
		if _, known := catalog.Lookup(k); !known {
			v, _ := env.Get(k)
			d.Other = append(d.Other, settingField{Key: catalog.Key{Name: k}, Value: v, Set: true})
		}
	}
	return d
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f, env, ok := s.envFile(w, c)
	if !ok {
		return
	}
	s.render(w, c, "settings", "Settings", "settings", s.buildSettings(f, env, nil, nil))
}

func (s *Server) settingsSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f, env, ok := s.envFile(w, c)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	cur := env.Map()
	values := map[string]string{}
	fieldErrs := map[string]string{}
	var changed []string
	for _, k := range catalog.Keys {
		old, has := cur[k.Name]
		switch {
		case k.Kind == catalog.Bool:
			on := r.PostForm.Has(k.Name)
			values[k.Name] = map[bool]string{true: "true", false: ""}[on]
			if on == catalog.IsTrue(old) {
				continue
			}
			if on {
				_ = env.Set(k.Name, "true")
			} else {
				env.Delete(k.Name)
			}
		case k.Sensitive:
			if r.PostForm.Has("clear_" + k.Name) {
				if !has {
					continue
				}
				env.Delete(k.Name)
				break
			}
			v := r.PostFormValue(k.Name)
			if v == "" || v == old {
				continue
			}
			if err := k.Validate(v); err != nil {
				fieldErrs[k.Name] = err.Error()
				continue
			}
			_ = env.Set(k.Name, v)
		default:
			v := strings.TrimSpace(r.PostFormValue(k.Name))
			values[k.Name] = v
			if v == old {
				continue
			}
			if v == "" {
				if !has {
					continue
				}
				env.Delete(k.Name)
				break
			}
			if err := k.Validate(v); err != nil {
				fieldErrs[k.Name] = err.Error()
				continue
			}
			if err := env.Set(k.Name, v); err != nil {
				fieldErrs[k.Name] = err.Error()
				continue
			}
		}
		changed = append(changed, k.Name)
	}
	// cross-field checks on the result, only for changed keys
	if len(fieldErrs) == 0 {
		for _, e := range catalog.ValidateEnv(env.Map()) {
			for _, ch := range changed {
				if strings.Contains(e.Error(), ch) {
					fieldErrs[e.Key] = e.Err
				}
			}
		}
	}
	if len(fieldErrs) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		orig, _ := s.opts.Manager.Env()
		d := s.buildSettings(f, orig, values, fieldErrs)
		d.Errors = []string{"Some values are not valid. Nothing was staged."}
		s.render(w, c, "settings", "Settings", "settings", d)
		return
	}
	if len(changed) == 0 {
		s.flash(c, "info", "No changes.")
		s.redirect(w, r, "admin/settings")
		return
	}
	if err := s.opts.Manager.Stage(f, env.Bytes()); err != nil {
		s.flash(c, "error", err.Error())
		s.redirect(w, r, "admin/settings")
		return
	}
	s.audit(r, c.user.Username, "stage", f.Path, "ok", "settings: "+strings.Join(changed, ","))
	s.flash(c, "ok", "Staged changes: "+strings.Join(changed, ", ")+". Review and apply on the Changes page.")
	s.redirect(w, r, "admin/settings")
}

// raw editor

const secretPlaceholder = "__CREW_SECRET_UNCHANGED__"

type rawData struct {
	Content string
	Errors  []string
	Path    string
}

func maskRaw(env *envfile.File) string {
	out := envfile.Parse(env.Bytes())
	for _, k := range out.Keys() {
		if key, ok := catalog.Lookup(k); ok && key.Sensitive {
			_ = out.Set(k, secretPlaceholder)
		}
	}
	return string(out.Bytes())
}

func (s *Server) rawPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f, env, ok := s.envFile(w, c)
	if !ok {
		return
	}
	s.render(w, c, "raw", "Edit settings file", "settings", rawData{Content: maskRaw(env), Path: f.Path})
}

func (s *Server) rawSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f, env, ok := s.envFile(w, c)
	if !ok {
		return
	}
	content := strings.ReplaceAll(r.PostFormValue("content"), "\r\n", "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	edited := envfile.Parse([]byte(content))
	for _, k := range edited.Keys() {
		if v, _ := edited.Get(k); v == secretPlaceholder {
			if old, ok := env.Get(k); ok {
				_ = edited.Set(k, old)
			} else {
				edited.Delete(k)
			}
		}
	}
	var errs []string
	for _, l := range edited.Invalid() {
		errs = append(errs, "Line "+strconv.Itoa(l.Number)+" is not KEY=value: "+l.Text)
	}
	for _, e := range catalog.ValidateEnv(edited.Map()) {
		errs = append(errs, e.Error())
	}
	if len(errs) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, c, "raw", "Edit settings file", "settings", rawData{Content: content, Errors: errs, Path: f.Path})
		return
	}
	if err := s.opts.Manager.Stage(f, edited.Bytes()); err != nil {
		s.flash(c, "error", err.Error())
	} else {
		s.audit(r, c.user.Username, "stage", f.Path, "ok", "raw edit")
		s.flash(c, "ok", "Staged. Review and apply on the Changes page.")
	}
	s.redirect(w, r, "admin/settings/raw")
}

// accounts

type accountsData struct {
	Files []apply.File
	Keys  map[string]catalog.Key
}

func (s *Server) accountsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	var files []apply.File
	for _, f := range s.opts.Manager.Files() {
		if f.Kind == apply.KindAuth {
			files = append(files, f)
		}
	}
	s.render(w, c, "accounts", "Accounts", "accounts", accountsData{Files: files})
}

type accountRow struct {
	User      string
	Type      string
	Print     string
	Protected bool
	SameAs    []string
}

type accountFileData struct {
	File    apply.File
	Rows    []accountRow
	Staged  bool
	Invalid []int
}

func (s *Server) authFile(w http.ResponseWriter, r *http.Request, c *reqCtx) (apply.File, *htpasswd.File, bool) {
	f, ok := s.opts.Manager.FileByID(r.PathValue("id"))
	if !ok || f.Kind != apply.KindAuth {
		http.NotFound(w, r)
		return f, nil, false
	}
	data, _, err := s.opts.Manager.Read(f)
	if err != nil {
		s.render(w, c, "message", "Accounts", "accounts", "Cannot read "+f.Path+": "+err.Error())
		return f, nil, false
	}
	return f, htpasswd.Parse(data), true
}

func (s *Server) isProtected(f apply.File, user string) bool {
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

func (s *Server) accountFilePage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f, hf, ok := s.authFile(w, r, c)
	if !ok {
		return
	}
	_, staged, _ := s.opts.Manager.Read(f)
	d := accountFileData{File: f, Staged: staged, Invalid: hf.Invalid()}
	byHash := map[string][]string{}
	for _, e := range hf.Entries() {
		byHash[e.Hash] = append(byHash[e.Hash], e.User)
	}
	for _, e := range hf.Entries() {
		row := accountRow{User: e.User, Type: htpasswd.HashType(e.Hash), Print: htpasswd.Fingerprint(e.Hash), Protected: s.isProtected(f, e.User)}
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
	f, hf, ok := s.authFile(w, r, c)
	if !ok {
		return
	}
	back := "admin/accounts/" + f.ID
	action := r.PathValue("action")
	user := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	fail := func(msg string) {
		s.flash(c, "error", msg)
		s.redirect(w, r, back)
	}
	if err := htpasswd.ValidUsername(user); err != nil {
		fail(err.Error())
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
		if action == "reset" && s.isProtected(f, user) {
			fail(user + " is the Crew service account. Change its password in the Crew config and this file together, by hand.")
			return
		}
		if password == "" {
			pw, err := htpasswd.GeneratePassword(20)
			if err != nil {
				fail(err.Error())
				return
			}
			password, generated = pw, pw
		} else if len(password) < 8 {
			fail("Password must have at least 8 characters.")
			return
		}
		h, err := htpasswd.HashPassword(password)
		if err != nil {
			fail(err.Error())
			return
		}
		if err := hf.Set(user, h); err != nil {
			fail(err.Error())
			return
		}
	case "delete":
		if s.isProtected(f, user) {
			fail(user + " is the Crew service account and cannot be deleted.")
			return
		}
		if !hf.Delete(user) {
			fail("Account " + user + " not found.")
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err := s.opts.Manager.Stage(f, hf.Bytes()); err != nil {
		fail(err.Error())
		return
	}
	s.audit(r, c.user.Username, "stage", f.Path, "ok", "account "+action+": "+user)
	msg := "Staged: " + action + " " + user + ". Review and apply on the Changes page."
	if generated != "" {
		s.sessions.addFlash(c.sess, flash{Kind: "ok", Text: msg + " Generated password (shown only once):", Secret: generated})
	} else {
		s.flash(c, "ok", msg)
	}
	s.redirect(w, r, back)
}

// changes and apply

type changesData struct {
	Changes  []apply.Change
	Problems []string
	Last     *apply.Result
}

func (s *Server) changesPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	d := changesData{Changes: s.opts.Manager.Pending(), Last: s.opts.Manager.LastResult()}
	if len(d.Changes) > 0 {
		d.Problems = s.opts.Manager.Validate()
	}
	s.render(w, c, "changes", "Changes", "changes", d)
}

func (s *Server) discard(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	if p := r.PostFormValue("path"); p != "" {
		s.opts.Manager.Discard(p)
		s.audit(r, c.user.Username, "discard", p, "ok", "")
		s.flash(c, "info", "Discarded changes to "+p+".")
	} else {
		s.opts.Manager.DiscardAll()
		s.audit(r, c.user.Username, "discard", "all", "ok", "")
		s.flash(c, "info", "Discarded all changes.")
	}
	s.redirect(w, r, "admin/changes")
}

func (s *Server) applySubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	// not tied to the request: a closed browser tab must not stop an apply halfway
	ctx, cancel := context.WithTimeout(context.Background(), 3*s.opts.ApplyTimeout)
	defer cancel()
	res, err := s.opts.Manager.Apply(ctx, c.user.Username)
	if err != nil {
		var ve *apply.ValidationError
		if errors.As(err, &ve) {
			s.sessions.addFlash(c.sess, flash{Kind: "error", Text: "Validation failed. Nothing was changed.", List: ve.Problems})
		} else {
			s.flash(c, "error", err.Error())
		}
		s.audit(r, c.user.Username, "apply", "", "refused", err.Error())
		s.redirect(w, r, "admin/changes")
		return
	}
	files := strings.Join(res.Files, ", ")
	if res.OK {
		s.audit(r, c.user.Username, "apply", files, "ok", "backup "+res.BackupID)
		s.flash(c, "ok", "Applied and Mailpit restarted: "+files+".")
	} else {
		detail := res.Error
		if res.RolledBack {
			s.sessions.addFlash(c.sess, flash{Kind: "error", Text: "Apply failed and was rolled back: " + res.Error, List: res.LogTail})
		} else {
			detail += "; rollback: " + res.RollbackError
			s.sessions.addFlash(c.sess, flash{Kind: "error", Text: "Apply failed and rollback had problems. Check Mailpit now. " + res.Error + " / " + res.RollbackError + " (backup " + res.BackupID + ")", List: res.LogTail})
		}
		s.audit(r, c.user.Username, "apply", files, "failed", detail)
	}
	s.redirect(w, r, "admin/changes")
}
