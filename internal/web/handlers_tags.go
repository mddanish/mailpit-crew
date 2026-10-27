package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/tagsfile"
)

type tagRow struct {
	Index int
	Match string
	Tags  string
}

type tagsData struct {
	File     apply.File
	Rows     []tagRow
	Errors   []string
	Staged   bool
	NotFound bool
}

const newTagRows = 3

func (s *Server) tagsFile(w http.ResponseWriter, c *reqCtx) (apply.File, *tagsfile.File, bool) {
	f, ok := s.opts.Manager.FileByID("tags")
	if !ok {
		s.render(w, c, "message", "Tag rules", "tags", "No tags file is configured. Set MP_TAGS_CONFIG on the Settings page (for example /opt/mailpit/tags.yaml), apply, then come back.")
		return f, nil, false
	}
	data, _, err := s.opts.Manager.Read(f)
	if err != nil {
		s.render(w, c, "message", "Tag rules", "tags", "Cannot read "+f.Path+": "+err.Error())
		return f, nil, false
	}
	tf, err := tagsfile.Parse(data)
	if err != nil {
		s.render(w, c, "message", "Tag rules", "tags", f.Path+" is not a valid tags file: "+err.Error()+". Fix it by hand first.")
		return f, nil, false
	}
	return f, tf, true
}

func rowsFor(rules []tagsfile.Rule) []tagRow {
	var rows []tagRow
	for i, r := range rules {
		rows = append(rows, tagRow{Index: i, Match: r.Match, Tags: r.Tags})
	}
	for i := 0; i < newTagRows; i++ {
		rows = append(rows, tagRow{Index: len(rules) + i})
	}
	return rows
}

func (s *Server) tagsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f, tf, ok := s.tagsFile(w, c)
	if !ok {
		return
	}
	_, staged, _ := s.opts.Manager.Read(f)
	s.render(w, c, "tags", "Tag rules", "tags", tagsData{File: f, Rows: rowsFor(tf.Rules()), Staged: staged, NotFound: !f.Exists})
}

func (s *Server) tagsSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	f, tf, ok := s.tagsFile(w, c)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	n, _ := strconv.Atoi(r.PostFormValue("rows"))
	if n < 0 || n > 10000 {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	type posRule struct {
		pos  float64
		rule tagsfile.Rule
	}
	var list []posRule
	var rows []tagRow
	for i := 0; i < n; i++ {
		k := strconv.Itoa(i)
		m := strings.TrimSpace(r.PostFormValue("match_" + k))
		t := strings.TrimSpace(r.PostFormValue("tags_" + k))
		if r.PostForm.Has("del_"+k) || (m == "" && t == "") {
			continue
		}
		pos, err := strconv.ParseFloat(r.PostFormValue("pos_"+k), 64)
		if err != nil {
			pos = float64(i + 1)
		}
		list = append(list, posRule{pos, tagsfile.Rule{Match: m, Tags: t}})
		rows = append(rows, tagRow{Index: len(rows), Match: m, Tags: t})
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].pos < list[b].pos })
	rules := make([]tagsfile.Rule, len(list))
	for i, p := range list {
		rules[i] = p.rule
	}
	if errs := tagsfile.Validate(rules); len(errs) > 0 {
		var msgs []string
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		w.WriteHeader(http.StatusBadRequest)
		for i := 0; i < newTagRows; i++ {
			rows = append(rows, tagRow{Index: len(rows)})
		}
		s.render(w, c, "tags", "Tag rules", "tags", tagsData{File: f, Rows: rows, Errors: msgs, NotFound: !f.Exists})
		return
	}
	tf.SetRules(rules)
	data, err := tf.Bytes()
	if err == nil {
		err = s.opts.Manager.Stage(f, data)
	}
	if err != nil {
		s.flash(c, "error", err.Error())
	} else {
		s.audit(r, c.user.Username, "stage", f.Path, "ok", "tag rules: "+strconv.Itoa(len(rules)))
		s.flash(c, "ok", "Staged "+strconv.Itoa(len(rules))+" tag rules. Review and apply on the Changes page.")
	}
	s.redirect(w, r, "admin/tags")
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
