package web

import (
	"net/http"
	"strings"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/catalog"
	"github.com/mddanish/mailpit-crew/internal/envfile"
	"github.com/mddanish/mailpit-crew/internal/verify"
)

// editorData feeds the full editor partial.
type editorData struct {
	Kind     string // URL name: env, tags, relay, forward
	Lang     string
	Content  string
	Base     string
	Issues   []verify.Issue
	Examples []Example
}

type settingRow struct {
	Key     string
	Value   string
	Desc    string
	Locked  bool
	Secret  bool
	Unknown bool
}

type keyOption struct {
	Name      string   `json:"name"`
	Desc      string   `json:"desc"`
	Kind      string   `json:"kind"`
	Example   string   `json:"example"`
	Default   string   `json:"default"`
	Options   []string `json:"options,omitempty"`
	Sensitive bool     `json:"sensitive,omitempty"`
	Current   string   `json:"current,omitempty"`
}

type keyGroup struct {
	Section string
	Keys    []keyOption
}

type settingsData struct {
	Mode   string
	Path   string
	Base   string
	Rows   []settingRow
	Groups []keyGroup
	Editor editorData
	Select string // key to preselect in the form
}

func modeOf(r *http.Request) string {
	if r.URL.Query().Get("mode") == "full" {
		return "full"
	}
	return "form"
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	s.renderSettings(w, c, modeOf(r), r.URL.Query().Get("key"), nil, http.StatusOK)
}

// renderSettings shows the settings page. editor, when set, replaces the
// full editor content (after a failed save).
func (s *Server) renderSettings(w http.ResponseWriter, c *reqCtx, mode, sel string, editor *editorData, status int) {
	path := s.opts.Manager.EnvPath()
	data, base, err := s.opts.Manager.Read(path)
	if err != nil {
		s.render(w, c, "message", "Settings", "settings", "Cannot read "+path+": "+err.Error())
		return
	}
	env := envfile.Parse(data)
	locked := s.opts.Manager.Locked()
	d := settingsData{Mode: mode, Path: path, Base: base, Select: sel}
	cur := env.Map()
	for _, k := range env.Keys() {
		v := cur[k]
		key, known := catalog.Lookup(k)
		_, isLocked := locked[k]
		row := settingRow{Key: k, Value: v, Desc: key.Desc, Locked: isLocked, Secret: key.Sensitive, Unknown: !known}
		if row.Secret {
			row.Value = "••••••••"
		}
		d.Rows = append(d.Rows, row)
	}
	for _, sec := range catalog.Sections {
		g := keyGroup{Section: sec}
		for _, k := range catalog.BySection(sec) {
			if _, isLocked := locked[k.Name]; isLocked {
				continue
			}
			o := keyOption{Name: k.Name, Desc: k.Desc, Kind: string(k.Kind), Example: catalog.Example(k.Name), Default: k.Default, Options: k.Options, Sensitive: k.Sensitive}
			if !k.Sensitive {
				o.Current = cur[k.Name]
			}
			g.Keys = append(g.Keys, o)
		}
		d.Groups = append(d.Groups, g)
	}
	if editor != nil {
		d.Editor = *editor
	} else {
		d.Editor = editorData{Content: maskSecrets(apply.KindEnv, string(data)), Base: base}
	}
	d.Editor.Kind, d.Editor.Lang = "env", "properties"
	d.Editor.Examples = examplesFor("env", s.opts.Managed.Layout.Dir)
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	s.render(w, c, "settings", "Settings", "settings", d)
}

func (s *Server) settingsSet(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	key := strings.TrimSpace(r.PostFormValue("key"))
	value := strings.TrimSpace(r.PostFormValue("value"))
	k, known := catalog.Lookup(key)
	fail := func(msg string) {
		s.flash(c, "error", msg)
		s.redirect(w, r, "admin/settings?key="+key)
	}
	if !known {
		fail("Choose a setting from the list. Use the full editor for other keys.")
		return
	}
	if _, isLocked := s.opts.Manager.Locked()[key]; isLocked {
		fail(key + " is managed by Crew.")
		return
	}
	if k.Kind == catalog.Bool && !catalog.IsTrue(value) {
		value = "" // false is Mailpit's default for every switch
	}
	if value == "" && k.Kind != catalog.Bool {
		fail("Enter a value, or use Delete to remove " + key + ".")
		return
	}
	if err := k.Validate(value); err != nil {
		fail(key + ": " + err.Error())
		return
	}
	path := s.opts.Manager.EnvPath()
	data, _, _ := s.opts.Manager.Read(path)
	env := envfile.Parse(data)
	if value == "" {
		env.Delete(key)
	} else if err := env.Set(key, value); err != nil {
		fail(err.Error())
		return
	}
	res := s.saveFile(r, c, textKinds["env"], path, env.Bytes(), r.PostFormValue("base"), "set "+key)
	s.flashSave(c, res, "Saved "+key+".")
	s.redirect(w, r, "admin/settings")
}

func (s *Server) settingsDelete(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	key := r.PostFormValue("key")
	if _, isLocked := s.opts.Manager.Locked()[key]; isLocked {
		s.flash(c, "error", key+" is managed by Crew.")
		s.redirect(w, r, "admin/settings")
		return
	}
	path := s.opts.Manager.EnvPath()
	data, _, _ := s.opts.Manager.Read(path)
	env := envfile.Parse(data)
	if _, ok := env.Get(key); !ok {
		s.flash(c, "error", key+" is not set.")
		s.redirect(w, r, "admin/settings")
		return
	}
	env.Delete(key)
	res := s.saveFile(r, c, textKinds["env"], path, env.Bytes(), r.PostFormValue("base"), "delete "+key)
	s.flashSave(c, res, "Removed "+key+".")
	s.redirect(w, r, "admin/settings")
}
