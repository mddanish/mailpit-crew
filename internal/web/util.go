package web

import (
	"fmt"
	"strings"
	"time"

	"github.com/mddanish/mailpit-crew/internal/mailpit"
)

func formatAddr(a *mailpit.Address) string {
	if a == nil {
		return ""
	}
	if a.Name != "" {
		return a.Name + " <" + a.Address + ">"
	}
	return a.Address
}

func formatAddrs(l []*mailpit.Address) string {
	var out []string
	for _, a := range l {
		if s := formatAddr(a); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

// formatWhen shows today's times as HH:MM and older dates as YYYY-MM-DD.
func formatWhen(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	if y, m, d := time.Now().Date(); t.Year() == y && t.Month() == m && t.Day() == d {
		return t.Format("15:04")
	}
	return t.Format("2006-01-02")
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
