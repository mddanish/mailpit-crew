package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mddanish/mailpit-crew/internal/apply"
	"github.com/mddanish/mailpit-crew/internal/verify"
)

// textKind describes a file with a form and a full editor.
type textKind struct {
	Kind  apply.Kind
	Key   string // env key that enables the file ("" for the env file)
	Label string
	Lang  string // CodeMirror mode
	Nav   string
}

var textKinds = map[string]textKind{
	"env":     {apply.KindEnv, "", "Mailpit settings", "properties", "settings"},
	"tags":    {apply.KindTags, "MP_TAGS_CONFIG", "Tag rules", "yaml", "tags"},
	"relay":   {apply.KindRelay, "MP_SMTP_RELAY_CONFIG", "SMTP relay", "yaml", "relay"},
	"forward": {apply.KindForward, "MP_SMTP_FORWARD_CONFIG", "SMTP forward", "yaml", "forward"},
}

// resolve returns the path of a file and whether Mailpit's settings point
// to it. Disabled files use Crew's standard path.
func (s *Server) resolve(tk textKind) (string, bool) {
	if tk.Key == "" {
		return s.opts.Manager.EnvPath(), true
	}
	if env, err := s.opts.Manager.Env(); err == nil {
		if v, _ := env.Get(tk.Key); v != "" {
			return v, true
		}
	}
	return s.opts.Managed.Layout.StandardFile(tk.Key), false
}

// saveResult is the outcome of saving a file from an editor.
type saveResult struct {
	Issues []verify.Issue // validation problems (nothing saved)
	Err    error          // other errors (nothing saved)
}

func (r saveResult) ok() bool { return r.Err == nil && len(r.Issues) == 0 }

// saveFile validates and writes a file. When enable is set, the env key is
// pointed at the file in the same step, so a new file becomes part of
// Mailpit's settings.
func (s *Server) saveFile(r *http.Request, c *reqCtx, tk textKind, path string, content []byte, base string, what string) saveResult {
	_, enabled := s.resolve(tk)
	err := s.opts.Manager.Save(tk.Kind, path, content, base)
	var ve *apply.ValidationError
	if errors.As(err, &ve) {
		return saveResult{Issues: ve.Issues}
	}
	if err != nil {
		return saveResult{Err: err}
	}
	if tk.Key != "" && !enabled {
		if err := s.setEnv(tk.Key, path); err != nil {
			return saveResult{Err: errors.New("the file was saved, but turning it on in the Mailpit settings failed: " + err.Error())}
		}
		what += " and turned on " + tk.Key
	}
	s.audit(r, c.user.Username, "save", path, "ok", what)
	return saveResult{}
}

// setEnv sets or (value "") removes one key in the env file.
func (s *Server) setEnv(key, value string) error {
	env, err := s.opts.Manager.Env()
	if err != nil {
		return err
	}
	if value == "" {
		env.Delete(key)
	} else if err := env.Set(key, value); err != nil {
		return err
	}
	err = s.opts.Manager.Save(apply.KindEnv, s.opts.Manager.EnvPath(), env.Bytes(), "")
	var ve *apply.ValidationError
	if errors.As(err, &ve) {
		var msgs []string
		for _, i := range ve.Issues {
			msgs = append(msgs, i.String())
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	return err
}

// flashSave reports a save on the next page.
func (s *Server) flashSave(c *reqCtx, res saveResult, okText string) {
	switch {
	case res.Err != nil:
		s.flash(c, "error", res.Err.Error())
	case len(res.Issues) > 0:
		var list []string
		for _, i := range res.Issues {
			list = append(list, i.String())
		}
		s.sessions.addFlash(c.sess, flash{Kind: "error", Text: "Not saved:", List: list})
	default:
		s.flash(c, "ok", okText+" Restart Mailpit to make it active.")
	}
}
