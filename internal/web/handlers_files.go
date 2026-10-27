package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/tagsfile"
	"github.com/mddanish/mailpit-crew/internal/verify"
	"github.com/mddanish/mailpit-crew/internal/yamlmap"
)

type entryRow struct {
	Key    string
	Value  string
	Desc   string
	Secret bool
	Known  bool
}

type fieldOption struct {
	Key     string   `json:"name"`
	Desc    string   `json:"desc"`
	Kind    string   `json:"kind"`
	Example string   `json:"example"`
	Options []string `json:"options,omitempty"`
	Secret  bool     `json:"sensitive,omitempty"`
	Current string   `json:"current,omitempty"`
}

type fileData struct {
	Name     string // URL name: tags, relay, forward
	Label    string
	Key      string
	Path     string
	Enabled  bool
	Exists   bool
	Mode     string
	Base     string
	Rules    []tagsfile.Rule
	Entries  []entryRow
	Fields   []fieldOption
	Editor   editorData
	Problems []verify.Issue // problems in the file on disk
	ParseErr string
	Select   string
}

func fieldsFor(name string) []verify.Field {
	if name == "relay" {
		return verify.RelayFields
	}
	return verify.ForwardFields
}

func (s *Server) kindOf(w http.ResponseWriter, r *http.Request) (string, textKind, bool) {
	name := r.PathValue("kind")
	tk, ok := textKinds[name]
	if !ok || name == "env" {
		http.NotFound(w, r)
		return "", tk, false
	}
	return name, tk, true
}

func (s *Server) filePage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name, tk, ok := s.kindOf(w, r)
	if !ok {
		return
	}
	s.renderFile(w, c, name, tk, modeOf(r), r.URL.Query().Get("key"), nil, http.StatusOK)
}

func (s *Server) renderFile(w http.ResponseWriter, c *reqCtx, name string, tk textKind, mode, sel string, editor *editorData, status int) {
	path, enabled := s.resolve(tk)
	data, base, err := s.opts.Manager.Read(path)
	if err != nil {
		s.render(w, c, "message", tk.Label, tk.Nav, "Cannot read "+path+": "+err.Error())
		return
	}
	d := fileData{Name: name, Label: tk.Label, Key: tk.Key, Path: path, Enabled: enabled, Exists: data != nil, Mode: mode, Base: base, Select: sel}
	if data != nil {
		d.Problems = s.opts.Manager.Verify(tk.Kind, path, data)
	}
	switch name {
	case "tags":
		if tf, err := tagsfile.Parse(data); err != nil {
			d.ParseErr = err.Error()
		} else {
			d.Rules = tf.Rules()
		}
	default:
		fields := fieldsFor(name)
		ym, err := yamlmap.Parse(data)
		if err != nil {
			d.ParseErr = err.Error()
		} else {
			cur := map[string]string{}
			for _, e := range ym.Entries() {
				f, known := verify.FieldByKey(fields, e.Key)
				row := entryRow{Key: e.Key, Value: e.Value, Desc: f.Desc, Secret: f.Secret, Known: known}
				if row.Secret {
					row.Value = "••••••••"
				} else {
					cur[e.Key] = e.Value
				}
				d.Entries = append(d.Entries, row)
			}
			for _, f := range fields {
				d.Fields = append(d.Fields, fieldOption{Key: f.Key, Desc: f.Desc, Kind: string(f.Kind), Example: f.Example, Options: f.Options, Secret: f.Secret, Current: cur[f.Key]})
			}
		}
	}
	if d.ParseErr != "" && mode == "form" {
		d.Mode = "full" // the form cannot edit a file it cannot read
	}
	if editor != nil {
		d.Editor = *editor
	} else {
		content := maskSecrets(tk.Kind, string(data))
		if data == nil {
			// a new file starts from the first example (not masked: no real secrets)
			if ex := examplesFor(name, s.opts.Managed.Layout.Dir); len(ex) > 0 {
				content = ex[0].Content
			}
		}
		d.Editor = editorData{Content: content, Base: base}
	}
	d.Editor.Kind, d.Editor.Lang = name, tk.Lang
	d.Editor.Examples = examplesFor(name, s.opts.Managed.Layout.Dir)
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	s.render(w, c, "file", tk.Label, tk.Nav, d)
}

// fileSet adds a tag rule, or sets one relay/forward key.
func (s *Server) fileSet(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name, tk, ok := s.kindOf(w, r)
	if !ok {
		return
	}
	page := "admin/file/" + name
	path, _ := s.resolve(tk)
	data, _, _ := s.opts.Manager.Read(path)
	var out []byte
	var what string
	switch name {
	case "tags":
		rule := tagsfile.Rule{Match: strings.TrimSpace(r.PostFormValue("match")), Tags: strings.TrimSpace(r.PostFormValue("tags"))}
		if errs := tagsfile.Validate([]tagsfile.Rule{rule}); len(errs) > 0 {
			s.flash(c, "error", errs[0].Error())
			s.redirect(w, r, page)
			return
		}
		tf, err := tagsfile.Parse(data)
		if err != nil {
			s.flash(c, "error", err.Error())
			s.redirect(w, r, page)
			return
		}
		tf.SetRules(append(tf.Rules(), rule))
		if out, err = tf.Bytes(); err != nil {
			s.flash(c, "error", err.Error())
			s.redirect(w, r, page)
			return
		}
		what = "add rule " + rule.Match
	default:
		key := r.PostFormValue("key")
		value := strings.TrimSpace(r.PostFormValue("value"))
		f, known := verify.FieldByKey(fieldsFor(name), key)
		if !known {
			s.flash(c, "error", "Choose a key from the list. Use the full editor for other keys.")
			s.redirect(w, r, page)
			return
		}
		if value == "" {
			s.flash(c, "error", "Enter a value, or use Delete to remove "+key+".")
			s.redirect(w, r, page+"?key="+key)
			return
		}
		if err := verify.CheckValue(f, value); err != nil {
			s.flash(c, "error", err.Error())
			s.redirect(w, r, page+"?key="+key)
			return
		}
		ym, err := yamlmap.Parse(data)
		if err == nil {
			kind := yamlmap.String
			switch f.Kind {
			case verify.FBool:
				kind = yamlmap.Bool
			case verify.FInt:
				kind = yamlmap.Int
			}
			err = ym.Set(key, value, kind)
		}
		if err == nil {
			out, err = ym.Bytes()
		}
		if err != nil {
			s.flash(c, "error", err.Error())
			s.redirect(w, r, page)
			return
		}
		what = "set " + key
	}
	res := s.saveFile(r, c, tk, path, out, r.PostFormValue("base"), what)
	s.flashSave(c, res, "Saved.")
	s.redirect(w, r, page)
}

// fileDelete removes a tag rule (by index) or a relay/forward key.
func (s *Server) fileDelete(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name, tk, ok := s.kindOf(w, r)
	if !ok {
		return
	}
	page := "admin/file/" + name
	path, _ := s.resolve(tk)
	data, _, _ := s.opts.Manager.Read(path)
	var out []byte
	var what string
	var err error
	if name == "tags" {
		var tf *tagsfile.File
		if tf, err = tagsfile.Parse(data); err == nil {
			rules := tf.Rules()
			i, convErr := strconv.Atoi(r.PostFormValue("index"))
			if convErr != nil || i < 0 || i >= len(rules) {
				http.Error(w, "bad rule", http.StatusBadRequest)
				return
			}
			what = "delete rule " + rules[i].Match
			tf.SetRules(append(rules[:i:i], rules[i+1:]...))
			out, err = tf.Bytes()
		}
	} else {
		var ym *yamlmap.File
		key := r.PostFormValue("key")
		if ym, err = yamlmap.Parse(data); err == nil {
			if !ym.Delete(key) {
				http.Error(w, "unknown key", http.StatusBadRequest)
				return
			}
			what = "delete " + key
			out, err = ym.Bytes()
		}
	}
	if err != nil {
		s.flash(c, "error", err.Error())
		s.redirect(w, r, page)
		return
	}
	res := s.saveFile(r, c, tk, path, out, r.PostFormValue("base"), what)
	s.flashSave(c, res, "Deleted.")
	s.redirect(w, r, page)
}

// fileMove moves a tag rule up or down.
func (s *Server) fileMove(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name, tk, ok := s.kindOf(w, r)
	if !ok || name != "tags" {
		http.NotFound(w, r)
		return
	}
	path, _ := s.resolve(tk)
	data, _, _ := s.opts.Manager.Read(path)
	tf, err := tagsfile.Parse(data)
	if err != nil {
		s.flash(c, "error", err.Error())
		s.redirect(w, r, "admin/file/tags")
		return
	}
	rules := tf.Rules()
	i, convErr := strconv.Atoi(r.PostFormValue("index"))
	j := i - 1
	if r.PostFormValue("dir") == "down" {
		j = i + 1
	}
	if convErr != nil || i < 0 || i >= len(rules) || j < 0 || j >= len(rules) {
		s.redirect(w, r, "admin/file/tags")
		return
	}
	rules[i], rules[j] = rules[j], rules[i]
	tf.SetRules(rules)
	out, err := tf.Bytes()
	if err != nil {
		s.flash(c, "error", err.Error())
		s.redirect(w, r, "admin/file/tags")
		return
	}
	res := s.saveFile(r, c, tk, path, out, r.PostFormValue("base"), "move rule")
	if !res.ok() {
		s.flashSave(c, res, "")
	}
	s.redirect(w, r, "admin/file/tags")
}

// fileDisable removes the env key that points to the file. The file stays.
func (s *Server) fileDisable(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name, tk, ok := s.kindOf(w, r)
	if !ok {
		return
	}
	if err := s.setEnv(tk.Key, ""); err != nil {
		s.flash(c, "error", err.Error())
	} else {
		s.audit(r, c.user.Username, "disable", tk.Key, "ok", "")
		s.flash(c, "ok", "Turned off "+tk.Label+" ("+tk.Key+" removed; the file is kept). Restart Mailpit to make it active.")
	}
	s.redirect(w, r, "admin/file/"+name)
}

// tagsTest returns how many stored messages match a search query.
func (s *Server) tagsTest(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	w.Header().Set("Content-Type", "application/json")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "empty match"})
		return
	}
	ctx, cancel := s.ctx(r, 5*time.Second)
	defer cancel()
	n, err := s.opts.Mailpit.SearchCount(ctx, q)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]int{"count": n})
}

// editorContent reads the full editor text from a request, with secrets
// restored from disk.
func (s *Server) editorContent(r *http.Request, tk textKind, path string) (masked, real string) {
	masked = normalize(r.PostFormValue("content"))
	cur, _, _ := s.opts.Manager.Read(path)
	return masked, unmaskSecrets(tk.Kind, masked, string(cur))
}

// editSave saves the full editor. On problems the page is shown again with
// the text and the problem lines, so nothing typed is lost.
func (s *Server) editSave(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name := r.PathValue("kind")
	tk, ok := textKinds[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	path, _ := s.resolve(tk)
	masked, content := s.editorContent(r, tk, path)
	base := r.PostFormValue("base")
	res := s.saveFile(r, c, tk, path, []byte(content), base, "full editor")
	if res.ok() {
		s.flash(c, "ok", "Saved. Restart Mailpit to make it active.")
		if name == "env" {
			s.redirect(w, r, "admin/settings?mode=full")
		} else {
			s.redirect(w, r, "admin/file/"+name+"?mode=full")
		}
		return
	}
	issues := res.Issues
	if res.Err != nil {
		issues = []verify.Issue{{Msg: res.Err.Error()}}
	}
	ed := &editorData{Content: masked, Base: base, Issues: issues}
	if res.Err == apply.ErrConflict {
		ed.Base = "" // show the text again; saving now overwrites the newer file
	}
	if name == "env" {
		s.renderSettings(w, c, "full", "", ed, http.StatusBadRequest)
	} else {
		s.renderFile(w, c, name, tk, "full", "", ed, http.StatusBadRequest)
	}
}

// editVerify checks editor text without saving.
func (s *Server) editVerify(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	name := r.PathValue("kind")
	tk, ok := textKinds[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	path, _ := s.resolve(tk)
	_, content := s.editorContent(r, tk, path)
	issues := s.opts.Manager.Verify(tk.Kind, path, []byte(content))
	if issues == nil {
		issues = []verify.Issue{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": len(verify.Errors(issues)) == 0, "issues": issues})
}
