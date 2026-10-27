package web

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mddanish/mailpit-crew/internal/inbox"
	"github.com/mddanish/mailpit-crew/internal/mailpit"
)

const perPage = 50

type inboxData struct {
	Result *inbox.Result
	Query  string
	Tag    string
	Error  string
	NoRule bool // a non-admin user without rules
}

func (d inboxData) PageURL(root string, page int) string {
	v := url.Values{}
	if d.Query != "" {
		v.Set("q", d.Query)
	}
	if d.Tag != "" {
		v.Set("tag", d.Tag)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return root + "inbox"
	}
	return root + "inbox?" + v.Encode()
}

func (s *Server) inboxPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	d := inboxData{Query: q, Tag: tag, NoRule: !c.user.IsAdmin() && len(c.user.Rules) == 0}
	ctx, cancel := s.ctx(r, 20*time.Second)
	defer cancel()
	res, err := s.opts.Inbox.List(ctx, c.user, q, tag, page, perPage)
	if err != nil {
		d.Error = err.Error()
		res = &inbox.Result{Page: 1, Pages: 1}
	}
	d.Result = res
	s.render(w, c, "inbox", "Inbox", "inbox", d)
}

// inboxState is polled by the inbox page to show "new mail".
func (s *Server) inboxState(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := s.ctx(r, 20*time.Second)
	defer cancel()
	res, err := s.opts.Inbox.List(ctx, c.user, "", "", 1, 1)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"total": res.Total, "unread": res.Unread, "latest": res.Latest})
}

// owned loads a message the user may see, or writes 404.
func (s *Server) owned(w http.ResponseWriter, r *http.Request, c *reqCtx) (*mailpit.Message, bool) {
	ctx, cancel := s.ctx(r, 20*time.Second)
	defer cancel()
	m, err := s.opts.Inbox.Get(ctx, c.user, r.PathValue("id"))
	if errors.Is(err, mailpit.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		http.Error(w, "Mailpit is not reachable: "+err.Error(), http.StatusBadGateway)
		return nil, false
	}
	return m, true
}

type headerRow struct {
	Name   string
	Values []string
}

type mailData struct {
	M       *mailpit.Message
	Tab     string
	Remote  bool
	Headers []headerRow
	Raw     string
	RawCut  bool
}

func (s *Server) mailPage(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	m, ok := s.owned(w, r, c)
	if !ok {
		return
	}
	ctx, cancel := s.ctx(r, 20*time.Second)
	defer cancel()
	if err := s.opts.Mailpit.SetRead(ctx, []string{m.ID}, true); err == nil {
		s.opts.Inbox.Invalidate()
	}
	d := mailData{M: m, Tab: r.URL.Query().Get("tab"), Remote: r.URL.Query().Get("remote") == "1"}
	switch d.Tab {
	case "html", "text", "headers", "raw", "attachments":
	default:
		d.Tab = "text"
		if m.HTML != "" {
			d.Tab = "html"
		}
	}
	switch d.Tab {
	case "headers":
		h, err := s.opts.Mailpit.Headers(ctx, m.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, v := range h {
			d.Headers = append(d.Headers, headerRow{k, v})
		}
		sort.Slice(d.Headers, func(i, j int) bool { return d.Headers[i].Name < d.Headers[j].Name })
	case "raw":
		resp, err := s.opts.Mailpit.Raw(ctx, m.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		const max = 512 << 10
		b, _ := io.ReadAll(io.LimitReader(resp.Body, max+1))
		resp.Body.Close()
		d.RawCut = len(b) > max
		if d.RawCut {
			b = b[:max]
		}
		d.Raw = string(b)
	}
	title := m.Subject
	if title == "" {
		title = "(no subject)"
	}
	s.render(w, c, "mail", title, "inbox", d)
}

var cidRe = regexp.MustCompile(`(?i)cid:([^"'\s)>]+)`)

// mailHTML serves the HTML body for the sandboxed iframe. Inline images are
// embedded as data: URIs (the sandboxed frame has no Crew session), and
// remote content is blocked unless remote=1.
func (s *Server) mailHTML(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	m, ok := s.owned(w, r, c)
	if !ok {
		return
	}
	ctx, cancel := s.ctx(r, 30*time.Second)
	defer cancel()
	inline := map[string]string{}
	budget := 10 << 20
	for _, p := range m.Inline {
		if p.ContentID == "" || !strings.HasPrefix(strings.ToLower(p.ContentType), "image/") || int(p.Size) > budget {
			continue
		}
		resp, err := s.opts.Mailpit.Part(ctx, m.ID, p.PartID)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, int64(budget)))
		resp.Body.Close()
		budget -= len(b)
		inline[strings.ToLower(p.ContentID)] = "data:" + p.ContentType + ";base64," + base64.StdEncoding.EncodeToString(b)
	}
	body := cidRe.ReplaceAllStringFunc(m.HTML, func(match string) string {
		id := strings.ToLower(strings.Trim(match[4:], "<>"))
		if d, ok := inline[id]; ok {
			return d
		}
		return match
	})
	// sandbox: no scripts, no forms, unique origin; nothing loads from the
	// network unless the viewer asked for remote content
	csp := "sandbox; default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; frame-ancestors 'self'"
	if r.URL.Query().Get("remote") == "1" {
		csp = "sandbox; default-src 'none'; img-src data: https: http:; style-src 'unsafe-inline' https: http:; font-src data: https: http:; frame-ancestors 'self'"
	}
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, body)
}

func safeFilename(name, fallback string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, name)
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}

func (s *Server) mailRaw(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	m, ok := s.owned(w, r, c)
	if !ok {
		return
	}
	ctx, cancel := s.ctx(r, 60*time.Second)
	defer cancel()
	resp, err := s.opts.Mailpit.Raw(ctx, m.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": m.ID + ".eml"}))
	io.Copy(w, resp.Body)
}

func (s *Server) mailPart(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	m, ok := s.owned(w, r, c)
	if !ok {
		return
	}
	partID := r.PathValue("part")
	var part *mailpit.Part
	for _, list := range [][]mailpit.Part{m.Attachments, m.Inline} {
		for i := range list {
			if list[i].PartID == partID {
				part = &list[i]
			}
		}
	}
	if part == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := s.ctx(r, 5*time.Minute)
	defer cancel()
	resp, err := s.opts.Mailpit.Part(ctx, m.ID, part.PartID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	// always a download: attachment content never renders in Crew's origin
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": safeFilename(part.FileName, "part-"+part.PartID),
	}))
	io.Copy(w, resp.Body)
}

// mailAction handles read, unread and delete for one or more messages.
func (s *Server) mailAction(w http.ResponseWriter, r *http.Request, c *reqCtx) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	action := r.PostFormValue("action")
	ids := r.PostForm["id"]
	back := r.PostFormValue("back")
	if !strings.HasPrefix(back, "inbox") {
		back = "inbox"
	}
	if len(ids) == 0 {
		s.flash(c, "info", "No messages selected.")
		s.redirect(w, r, back)
		return
	}
	if len(ids) > 500 {
		http.Error(w, "too many messages", http.StatusBadRequest)
		return
	}
	ctx, cancel := s.ctx(r, 60*time.Second)
	defer cancel()
	owned, err := s.opts.Inbox.Owned(ctx, c.user, ids)
	if err != nil {
		s.flash(c, "error", err.Error())
		s.redirect(w, r, back)
		return
	}
	if len(owned) == 0 {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "read", "unread":
		err = s.opts.Mailpit.SetRead(ctx, owned, action == "read")
	case "delete":
		err = s.opts.Mailpit.Delete(ctx, owned)
		if err == nil {
			s.audit(r, c.user.Username, "delete-messages", strings.Join(owned, ","), "ok", "")
			if back != "inbox" && strings.HasPrefix(back, "inbox/") {
				back = "inbox"
			}
		}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	s.opts.Inbox.Invalidate()
	if err != nil {
		s.flash(c, "error", err.Error())
	} else if action == "delete" {
		s.flash(c, "ok", fmt.Sprintf("Deleted %d message(s).", len(owned)))
	}
	s.redirect(w, r, back)
}
