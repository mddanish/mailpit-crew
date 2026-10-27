package mailpit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// Address is an email address.
type Address struct {
	Name    string
	Address string
}

// Summary is a message in a list.
type Summary struct {
	ID          string
	MessageID   string
	Read        bool
	From        *Address
	To          []*Address
	Cc          []*Address
	Bcc         []*Address
	ReplyTo     []*Address
	Subject     string
	Created     time.Time
	Username    string
	Tags        []string
	Size        uint64
	Attachments int
	Snippet     string
}

// Part is an attachment or inline part.
type Part struct {
	PartID      string
	FileName    string
	ContentType string
	ContentID   string
	Size        uint64
}

// Message is a full message.
type Message struct {
	ID          string
	MessageID   string
	From        *Address
	To          []*Address
	Cc          []*Address
	Bcc         []*Address
	ReplyTo     []*Address
	ReturnPath  string
	Subject     string
	Date        time.Time
	Tags        []string
	Username    string
	Text        string
	HTML        string
	Size        uint64
	Inline      []Part
	Attachments []Part
}

// Summary returns the list form of a full message.
func (m *Message) Summary() Summary {
	return Summary{ID: m.ID, MessageID: m.MessageID, From: m.From, To: m.To, Cc: m.Cc, Bcc: m.Bcc,
		ReplyTo: m.ReplyTo, Subject: m.Subject, Created: m.Date, Username: m.Username, Tags: m.Tags,
		Size: m.Size, Attachments: len(m.Attachments)}
}

// Page is one page of a message list.
type Page struct {
	Total         int `json:"total"`
	MessagesCount int `json:"messages_count"`
	Messages      []Summary
}

// ErrNotFound is returned for unknown or invalid message IDs.
var ErrNotFound = errors.New("message not found")

// Mailpit IDs are short UUIDs. "latest" is a special Mailpit alias and is
// never accepted.
var idRe = regexp.MustCompile(`^[A-Za-z0-9]{10,40}$`)

// ValidID reports whether id looks like a Mailpit message ID.
func ValidID(id string) bool { return id != "latest" && idRe.MatchString(id) }

var partRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// ValidPartID reports whether id looks like a Mailpit part ID (1.2.3).
func ValidPartID(id string) bool { return partRe.MatchString(id) }

// List returns one page of all messages, newest first.
func (c *Client) List(ctx context.Context, start, limit int) (*Page, error) {
	var p Page
	q := "?start=" + strconv.Itoa(start) + "&limit=" + strconv.Itoa(limit)
	if err := c.get(ctx, "/api/v1/messages"+q, &p); err != nil {
		return nil, err
	}
	p.MessagesCount = p.Total
	return &p, nil
}

// Search returns one page of messages matching a Mailpit search query.
func (c *Client) Search(ctx context.Context, query string, start, limit int) (*Page, error) {
	var p Page
	q := "?query=" + url.QueryEscape(query) + "&start=" + strconv.Itoa(start) + "&limit=" + strconv.Itoa(limit)
	if err := c.get(ctx, "/api/v1/search"+q, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Message returns a full message. It does not mark it read.
func (c *Client) Message(ctx context.Context, id string) (*Message, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	var m Message
	if err := c.get(ctx, "/api/v1/message/"+id, &m); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// Headers returns the message headers.
func (c *Client) Headers(ctx context.Context, id string) (map[string][]string, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	var h map[string][]string
	return h, c.get(ctx, "/api/v1/message/"+id+"/headers", &h)
}

// Raw opens the raw message source. The caller closes the response body.
func (c *Client) Raw(ctx context.Context, id string) (*http.Response, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	return c.stream(ctx, "/api/v1/message/"+id+"/raw")
}

// Part opens an attachment or inline part. The caller closes the body.
func (c *Client) Part(ctx context.Context, id, partID string) (*http.Response, error) {
	if !ValidID(id) || !ValidPartID(partID) {
		return nil, ErrNotFound
	}
	return c.stream(ctx, "/api/v1/message/"+id+"/part/"+partID)
}

func (c *Client) stream(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("mailpit API: %s", resp.Status)
	}
	return resp, nil
}

// SetRead marks messages read or unread. An empty list is refused: Mailpit
// would update every message.
func (c *Client) SetRead(ctx context.Context, ids []string, read bool) error {
	if err := checkIDs(ids); err != nil {
		return err
	}
	return c.send(ctx, http.MethodPut, "/api/v1/messages", map[string]any{"IDs": ids, "Read": read})
}

// Delete deletes messages. An empty list is refused: Mailpit would delete
// every message.
func (c *Client) Delete(ctx context.Context, ids []string) error {
	if err := checkIDs(ids); err != nil {
		return err
	}
	return c.send(ctx, http.MethodDelete, "/api/v1/messages", map[string]any{"IDs": ids})
}

func checkIDs(ids []string) error {
	if len(ids) == 0 {
		return errors.New("no message IDs given")
	}
	for _, id := range ids {
		if !ValidID(id) {
			return fmt.Errorf("invalid message ID %q", id)
		}
	}
	return nil
}

func (c *Client) send(ctx context.Context, method, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mailpit API: %s", resp.Status)
	}
	return nil
}

type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return e.msg }

func isStatus(err error, code int) bool {
	var se statusError
	return errors.As(err, &se) && se.code == code
}
