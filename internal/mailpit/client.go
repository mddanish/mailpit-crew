// Package mailpit is a small client for the Mailpit HTTP API.
package mailpit

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client talks to one Mailpit instance.
type Client struct {
	mu       sync.RWMutex
	base     string
	user     string
	password string
	http     *http.Client
}

// New returns a client. base is the Mailpit URL, for example
// http://127.0.0.1:8025.
func New(base, user, password string) *Client {
	c := &Client{user: user, password: password}
	c.SetBase(base)
	return c
}

// SetBase changes the Mailpit URL. For https the certificate is not
// verified: Crew talks to its own Mailpit, whose certificate is issued for
// the public host name, not for 127.0.0.1.
func (c *Client) SetBase(base string) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- own Mailpit, see above
	c.mu.Lock()
	c.base = strings.TrimRight(base, "/")
	c.http = &http.Client{Timeout: 10 * time.Second, Transport: tr}
	c.mu.Unlock()
}

// Base returns the Mailpit URL.
func (c *Client) Base() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.base
}

func (c *Client) conn() (string, *http.Client) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.base, c.http
}

// Info is the subset of /api/v1/info that Crew uses.
type Info struct {
	Version       string
	LatestVersion string
	Database      string
	DatabaseSize  uint64
	Messages      uint64
	Unread        uint64
	Tags          map[string]int64
	RuntimeStats  struct {
		Uptime       uint64
		SMTPAccepted uint64
		SMTPRejected uint64
	}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	base, hc := c.conn()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusUnauthorized {
			return statusError{resp.StatusCode, "mailpit API: unauthorized (check the Crew service account)"}
		}
		return statusError{resp.StatusCode, "mailpit API: " + resp.Status}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// Ready returns nil when Mailpit reports ready (GET /readyz).
func (c *Client) Ready(ctx context.Context) error {
	return c.get(ctx, "/readyz", nil)
}

// Info returns application information.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	var i Info
	if err := c.get(ctx, "/api/v1/info", &i); err != nil {
		return nil, err
	}
	return &i, nil
}

// Uptime returns how long Mailpit has been running.
func (c *Client) Uptime(ctx context.Context) (time.Duration, error) {
	i, err := c.Info(ctx)
	if err != nil {
		return 0, err
	}
	return time.Duration(i.RuntimeStats.Uptime) * time.Second, nil
}

// SearchCount returns how many messages match a Mailpit search query.
func (c *Client) SearchCount(ctx context.Context, query string) (int, error) {
	var r struct {
		MessagesCount int `json:"messages_count"`
	}
	if err := c.get(ctx, "/api/v1/search?limit=1&query="+url.QueryEscape(query), &r); err != nil {
		return 0, err
	}
	return r.MessagesCount, nil
}

// WaitReady polls Ready until it succeeds or ctx ends.
func (c *Client) WaitReady(ctx context.Context) error {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	var last error
	for {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		last = c.Ready(rctx)
		cancel()
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("mailpit not ready: %v", last)
		case <-t.C:
		}
	}
}

type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return e.msg }
