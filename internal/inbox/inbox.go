package inbox

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mddanish/mailpit-crew/internal/mailpit"
	"github.com/mddanish/mailpit-crew/internal/users"
)

// Inbox reads Mailpit and filters it per user.
type Inbox struct {
	Client  *mailpit.Client
	MaxScan int           // most messages read per list (default 10000)
	TTL     time.Duration // list cache lifetime (default 5s)

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at        time.Time
	msgs      []mailpit.Summary
	truncated bool
}

// Result is one page of a user's inbox.
type Result struct {
	Messages  []mailpit.Summary
	Total     int // visible messages matching the query
	Unread    int
	Page      int
	Pages     int
	Tags      []string // tags on visible messages
	Truncated bool     // Mailpit has more messages than MaxScan
	Latest    string   // newest visible message ID
}

const pageSize = 250

func (i *Inbox) maxScan() int {
	if i.MaxScan > 0 {
		return i.MaxScan
	}
	return 10000
}

// all returns every message for a query (empty = all), newest first.
func (i *Inbox) all(ctx context.Context, query string) ([]mailpit.Summary, bool, error) {
	ttl := i.TTL
	if ttl == 0 {
		ttl = 5 * time.Second
	}
	i.mu.Lock()
	if c, ok := i.cache[query]; ok && time.Since(c.at) < ttl {
		i.mu.Unlock()
		return c.msgs, c.truncated, nil
	}
	i.mu.Unlock()

	var out []mailpit.Summary
	truncated := false
	for start := 0; ; start += pageSize {
		var p *mailpit.Page
		var err error
		if query == "" {
			p, err = i.Client.List(ctx, start, pageSize)
		} else {
			p, err = i.Client.Search(ctx, query, start, pageSize)
		}
		if err != nil {
			return nil, false, err
		}
		out = append(out, p.Messages...)
		if len(p.Messages) < pageSize || len(out) >= p.MessagesCount {
			break
		}
		if len(out) >= i.maxScan() {
			truncated = true
			break
		}
	}
	i.mu.Lock()
	if i.cache == nil || len(i.cache) > 100 {
		i.cache = map[string]cached{}
	}
	i.cache[query] = cached{at: time.Now(), msgs: out, truncated: truncated}
	i.mu.Unlock()
	return out, truncated, nil
}

// Invalidate drops cached lists (after deletes or read changes).
func (i *Inbox) Invalidate() {
	i.mu.Lock()
	i.cache = nil
	i.mu.Unlock()
}

// List returns one page of the messages u may see. query is passed to
// Mailpit search; tag filters by tag.
func (i *Inbox) List(ctx context.Context, u users.User, query, tag string, page, perPage int) (*Result, error) {
	msgs, truncated, err := i.all(ctx, strings.TrimSpace(query))
	if err != nil {
		return nil, err
	}
	res := &Result{Truncated: truncated}
	tagSet := map[string]bool{}
	var visible []mailpit.Summary
	for k := range msgs {
		m := &msgs[k]
		if !Visible(u, m) {
			continue
		}
		for _, t := range m.Tags {
			tagSet[t] = true
		}
		if tag != "" && !hasTag(m.Tags, tag) {
			continue
		}
		if res.Latest == "" {
			res.Latest = m.ID
		}
		if !m.Read {
			res.Unread++
		}
		visible = append(visible, *m)
	}
	for t := range tagSet {
		res.Tags = append(res.Tags, t)
	}
	sortStrings(res.Tags)
	if perPage < 1 {
		perPage = 50
	}
	res.Total = len(visible)
	res.Pages = (res.Total + perPage - 1) / perPage
	if res.Pages == 0 {
		res.Pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > res.Pages {
		page = res.Pages
	}
	res.Page = page
	from := (page - 1) * perPage
	to := from + perPage
	if to > len(visible) {
		to = len(visible)
	}
	if from < to {
		res.Messages = visible[from:to]
	}
	return res, nil
}

func hasTag(tags []string, t string) bool {
	for _, x := range tags {
		if strings.EqualFold(x, t) {
			return true
		}
	}
	return false
}

func sortStrings(s []string) {
	sort.Slice(s, func(i, j int) bool { return strings.ToLower(s[i]) < strings.ToLower(s[j]) })
}

// Get returns a message u may see, or mailpit.ErrNotFound.
func (i *Inbox) Get(ctx context.Context, u users.User, id string) (*mailpit.Message, error) {
	m, err := i.Client.Message(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.ID != id {
		return nil, mailpit.ErrNotFound
	}
	s := m.Summary()
	if !Visible(u, &s) {
		return nil, mailpit.ErrNotFound
	}
	return m, nil
}

// Owned returns the subset of ids that u may see. Unknown IDs are dropped.
func (i *Inbox) Owned(ctx context.Context, u users.User, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		if _, err := i.Get(ctx, u, id); err != nil {
			if errors.Is(err, mailpit.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}
