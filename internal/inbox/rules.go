// Package inbox gives each Crew user a filtered view of Mailpit's single
// mailbox, based on ownership rules.
package inbox

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/mddanish/mailpit-crew/internal/mailpit"
	"github.com/mddanish/mailpit-crew/internal/users"
)

// Rule types.
const (
	RuleSMTPUser = "smtp_user" // SMTP login that sent the message
	RuleFrom     = "from"      // sender address, glob
	RuleTo       = "to"        // any To, Cc or Bcc address, glob
	RuleTag      = "tag"       // Mailpit tag
)

// RuleTypes lists rule types with labels, in display order.
var RuleTypes = []struct{ Type, Label, Hint string }{
	{RuleSMTPUser, "SMTP account", "gitea"},
	{RuleFrom, "From address", "*@forge.example.com"},
	{RuleTo, "To / Cc / Bcc address", "qa+*@example.com"},
	{RuleTag, "Tag", "Vaultwarden"},
}

// ValidateRule checks a rule.
func ValidateRule(r users.Rule) error {
	v := strings.TrimSpace(r.Value)
	if v == "" {
		return fmt.Errorf("%s rule needs a value", r.Type)
	}
	if len(v) > 254 || strings.ContainsAny(v, "\n\r") {
		return fmt.Errorf("%s rule value is too long or has line breaks", r.Type)
	}
	switch r.Type {
	case RuleSMTPUser, RuleTag:
		return nil
	case RuleFrom, RuleTo:
		if strings.ContainsAny(v, " \t,") {
			return fmt.Errorf("%s rule must be one address pattern (no spaces or commas)", r.Type)
		}
		return nil
	}
	return fmt.Errorf("unknown rule type %q", r.Type)
}

var (
	globMu    sync.Mutex
	globCache = map[string]*regexp.Regexp{}
)

// glob compiles * and ? patterns, case-insensitive, matching the whole value.
func glob(pattern string) *regexp.Regexp {
	globMu.Lock()
	defer globMu.Unlock()
	if re, ok := globCache[pattern]; ok {
		return re
	}
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	if len(globCache) > 10000 {
		globCache = map[string]*regexp.Regexp{}
	}
	globCache[pattern] = re
	return re
}

func addrs(lists ...[]*mailpit.Address) []string {
	var out []string
	for _, l := range lists {
		for _, a := range l {
			if a != nil && a.Address != "" {
				out = append(out, a.Address)
			}
		}
	}
	return out
}

// Matches reports whether a rule matches a message.
func Matches(r users.Rule, m *mailpit.Summary) bool {
	v := strings.TrimSpace(r.Value)
	if v == "" {
		return false
	}
	switch r.Type {
	case RuleSMTPUser:
		if strings.EqualFold(m.Username, v) {
			return true
		}
		// older Mailpit: the username is only known as a tag (MP_TAGS_USERNAME)
		if m.Username == "" {
			for _, t := range m.Tags {
				if strings.EqualFold(t, v) {
					return true
				}
			}
		}
	case RuleFrom:
		return m.From != nil && glob(v).MatchString(m.From.Address)
	case RuleTo:
		re := glob(v)
		for _, a := range addrs(m.To, m.Cc, m.Bcc) {
			if re.MatchString(a) {
				return true
			}
		}
	case RuleTag:
		for _, t := range m.Tags {
			if strings.EqualFold(t, v) {
				return true
			}
		}
	}
	return false
}

// Visible reports whether a user may see a message. Admins see everything;
// other users see messages that match at least one of their rules.
func Visible(u users.User, m *mailpit.Summary) bool {
	if !u.IsEnabled() {
		return false
	}
	if u.IsAdmin() {
		return true
	}
	for _, r := range u.Rules {
		if Matches(r, m) {
			return true
		}
	}
	return false
}
