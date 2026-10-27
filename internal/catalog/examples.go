package catalog

// examples are sample values shown in the settings form.
var examples = map[string]string{
	"MP_LABEL":                   "Home Lab Mailpit",
	"MP_TENANT_ID":               "team-a",
	"MP_MAX_MESSAGES":            "1000",
	"MP_MAX_AGE":                 "30d",
	"MP_COMPRESSION":             "1",
	"MP_UI_BIND_ADDR":            "[::]:8025",
	"MP_UI_TLS_CERT":             "/opt/mailpit-crew/mailpit/ssl/mail.example.com.fullchain.pem",
	"MP_UI_TLS_KEY":              "/opt/mailpit-crew/mailpit/ssl/mail.example.com.key.pem",
	"MP_API_CORS":                "https://app.example.com",
	"MP_SMTP_BIND_ADDR":          "[::]:1025",
	"MP_SMTP_TLS_CERT":           "/opt/mailpit-crew/mailpit/ssl/mail.example.com.fullchain.pem",
	"MP_SMTP_TLS_KEY":            "/opt/mailpit-crew/mailpit/ssl/mail.example.com.key.pem",
	"MP_SMTP_MAX_RECIPIENTS":     "100",
	"MP_SMTP_ALLOWED_RECIPIENTS": `@example\.com$`,
	"MP_SMTP_RELAY_MATCHING":     `@example\.com$`,
	"MP_SMTP_RELAY_HOST":         "smtp.example.com",
	"MP_SMTP_RELAY_PORT":         "587",
	"MP_SMTP_RELAY_AUTH":         "plain",
	"MP_SMTP_RELAY_USERNAME":     "relay@example.com",
	"MP_SMTP_FORWARD_TO":         "dev@example.com",
	"MP_SMTP_FORWARD_HOST":       "smtp.example.com",
	"MP_SMTP_FORWARD_PORT":       "587",
	"MP_POP3_BIND_ADDR":          "[::]:1110",
	"MP_TAG":                     `"Vault=from:vault@example.com"`,
	"MP_TAGS_DISABLE":            "plus-addresses,x-tags",
	"MP_ENABLE_SPAMASSASSIN":     "127.0.0.1:783",
	"MP_ENABLE_PROMETHEUS":       "0.0.0.0:9090",
	"MP_WEBHOOK_URL":             "https://hooks.example.com/mailpit",
	"MP_WEBHOOK_LIMIT":           "1",
	"MP_CHAOS_TRIGGERS":          "Sender:451:10",
}

// Example returns a sample value for a key ("" when none).
func Example(name string) string {
	if v, ok := examples[name]; ok {
		return v
	}
	if k, ok := Lookup(name); ok && k.Kind == Bool {
		return "true"
	}
	return ""
}
