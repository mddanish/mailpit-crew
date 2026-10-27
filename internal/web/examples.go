package web

import "strings"

// Example is a snippet the full editor can insert.
type Example struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// examplesFor returns snippets for a file kind. dir is the managed Mailpit
// directory, used in file paths.
func examplesFor(kind, dir string) []Example {
	var out []Example
	for _, e := range examples[kind] {
		e.Content = strings.ReplaceAll(e.Content, "{dir}", dir)
		out = append(out, e)
	}
	return out
}

var examples = map[string][]Example{
	"env": {
		{"Limit stored mail", "# keep at most 1000 messages, none older than 30 days\nMP_MAX_MESSAGES=1000\nMP_MAX_AGE=30d\n"},
		{"Web UI with HTTPS", "# Mailpit web UI on port 8025 with a certificate\nMP_UI_BIND_ADDR=[::]:8025\nMP_UI_TLS_CERT={dir}/ssl/mail.example.com.fullchain.pem\nMP_UI_TLS_KEY={dir}/ssl/mail.example.com.key.pem\n"},
		{"SMTP login over TLS", "# apps must log in, over STARTTLS\nMP_SMTP_AUTH_FILE={dir}/smtp.auth\nMP_SMTP_TLS_CERT={dir}/ssl/mail.example.com.fullchain.pem\nMP_SMTP_TLS_KEY={dir}/ssl/mail.example.com.key.pem\nMP_SMTP_REQUIRE_STARTTLS=true\n"},
		{"SMTP login without TLS (lab only)", "# allow PLAIN/LOGIN without encryption\nMP_SMTP_AUTH_FILE={dir}/smtp.auth\nMP_SMTP_AUTH_ALLOW_INSECURE=true\n"},
		{"Only accept mail for one domain", "MP_SMTP_ALLOWED_RECIPIENTS=@example\\.com$\n"},
		{"Tags from SMTP login and tag file", "MP_TAGS_CONFIG={dir}/tags.yaml\nMP_TAGS_USERNAME=true\nMP_TAGS_TITLE_CASE=true\n"},
		{"POP3 for mail clients", "MP_POP3_BIND_ADDR=[::]:1110\nMP_POP3_AUTH_FILE={dir}/pop3.auth\n"},
		{"Webhook on new mail", "MP_WEBHOOK_URL=https://hooks.example.com/mailpit\nMP_WEBHOOK_LIMIT=1\n"},
	},
	"tags": {
		{"Empty tag file", "filters: []\n"},
		{"Tag by sender", "filters:\n  # mail from Gitea gets the tags Gitea and Git\n  - match: from:gitea@example.com\n    tags: Gitea, Git\n"},
		{"Tag by recipient", "filters:\n  # mail to, cc or bcc this address\n  - match: addressed:qa@example.com\n    tags: QA\n"},
		{"Tag by subject words", "filters:\n  # all words, any order\n  - match: subject:password subject:reset\n    tags: Password reset\n"},
		{"Tag an exact phrase", "filters:\n  # note the quoting for an exact phrase\n  - match: '\"order confirmation\"'\n    tags: Orders\n"},
		{"Several rules", "filters:\n  - match: from:gitea@example.com\n    tags: Gitea\n  - match: from:vaultwarden@example.com\n    tags: Vaultwarden\n  - match: has:attachment\n    tags: Attachment\n"},
	},
	"relay": {
		{"Relay with STARTTLS and login", "# release mail through your provider\nhost: smtp.example.com\nport: 587\nstarttls: true\nauth: plain\nusername: relay@example.com\npassword: app-password\n# only release to your own domain\nallowed-recipients: '@example\\.com$'\n"},
		{"Relay over SSL/TLS (port 465)", "host: smtp.example.com\nport: 465\ntls: true\nauth: login\nusername: relay@example.com\npassword: app-password\n"},
		{"Local relay without login", "# a relay on the same network, no auth\nhost: 192.168.1.10\nport: 25\nauth: none\n"},
		{"Relay with fixed sender", "host: smtp.example.com\nport: 587\nstarttls: true\nauth: plain\nusername: relay@example.com\npassword: app-password\noverride-from: mailpit@example.com\nblocked-recipients: '@(gmail|yahoo)\\.com$'\n"},
	},
	"forward": {
		{"Forward a copy of all mail", "# every message Mailpit receives is also sent to these addresses\nto: dev@example.com, qa@example.com\nhost: smtp.example.com\nport: 587\nstarttls: true\nauth: plain\nusername: forward@example.com\npassword: app-password\n"},
		{"Forward through a local relay", "to: archive@example.com\nhost: 192.168.1.10\nport: 25\nauth: none\n"},
	},
}
