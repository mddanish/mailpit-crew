package managed

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Release is one Mailpit release.
type Release struct {
	Tag       string
	Published time.Time
}

// Releases lists and downloads Mailpit releases from GitHub.
type Releases struct {
	APIURL      string // https://api.github.com/repos/axllent/mailpit/releases
	FeedURL     string // https://github.com/axllent/mailpit/releases.atom (fallback)
	DownloadURL string // https://github.com/axllent/mailpit/releases/download
	HTTP        *http.Client

	mu       sync.Mutex
	cache    []Release
	cachedAt time.Time
}

var tagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// ValidTag reports whether s looks like a Mailpit release tag (v1.28.0).
func ValidTag(s string) bool { return tagRe.MatchString(s) }

func (r *Releases) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// Latest returns up to n newest stable releases, newest first. Results are
// cached for 15 minutes.
func (r *Releases) Latest(ctx context.Context, n int) ([]Release, error) {
	r.mu.Lock()
	if time.Since(r.cachedAt) < 15*time.Minute && len(r.cache) > 0 {
		out := r.cache
		r.mu.Unlock()
		return first(out, n), nil
	}
	r.mu.Unlock()

	list, err := r.fromAPI(ctx)
	if err != nil && r.FeedURL != "" {
		var ferr error
		if list, ferr = r.fromFeed(ctx); ferr != nil {
			return nil, fmt.Errorf("list Mailpit releases: %v; feed: %v", err, ferr)
		}
	} else if err != nil {
		return nil, fmt.Errorf("list Mailpit releases: %w", err)
	}
	sortReleases(list)
	r.mu.Lock()
	r.cache, r.cachedAt = list, time.Now()
	r.mu.Unlock()
	return first(list, n), nil
}

func first(l []Release, n int) []Release {
	if len(l) > n {
		return l[:n]
	}
	return l
}

func (r *Releases) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "mailpit-crew")
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

func (r *Releases) fromAPI(ctx context.Context) ([]Release, error) {
	resp, err := r.get(ctx, r.APIURL+"?per_page=20", "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw []struct {
		TagName     string    `json:"tag_name"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	var out []Release
	for _, x := range raw {
		if !x.Draft && !x.Prerelease && ValidTag(x.TagName) {
			out = append(out, Release{Tag: x.TagName, Published: x.PublishedAt})
		}
	}
	return out, nil
}

func (r *Releases) fromFeed(ctx context.Context) ([]Release, error) {
	resp, err := r.get(ctx, r.FeedURL, "application/atom+xml")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var feed struct {
		Entries []struct {
			Updated time.Time `xml:"updated"`
			Link    struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&feed); err != nil {
		return nil, err
	}
	var out []Release
	for _, e := range feed.Entries {
		if tag := path.Base(e.Link.Href); ValidTag(tag) {
			out = append(out, Release{Tag: tag, Published: e.Updated})
		}
	}
	return out, nil
}

// sortReleases sorts newest version first.
func sortReleases(l []Release) {
	sort.SliceStable(l, func(i, j int) bool { return CompareTags(l[i].Tag, l[j].Tag) > 0 })
}

// CompareTags compares two vX.Y.Z tags (-1, 0, 1). Invalid tags sort last.
func CompareTags(a, b string) int {
	pa, pb := parseTag(a), parseTag(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseTag(t string) [3]int {
	var out [3]int
	if !ValidTag(t) {
		return [3]int{-1, -1, -1}
	}
	for i, p := range strings.Split(strings.TrimPrefix(t, "v"), ".") {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

// AssetName is the release archive for this machine.
func AssetName() string {
	return "mailpit-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
}

// BinaryName is the file name of an installed release.
func BinaryName(tag string) string { return "mailpit-" + tag }

// Download fetches a release into binDir and returns the binary path. The
// binary must report the requested version before it is kept.
func (r *Releases) Download(ctx context.Context, tag, binDir string) (string, error) {
	if !ValidTag(tag) {
		return "", fmt.Errorf("invalid version %q, expected a tag like v1.28.0", tag)
	}
	dest := filepath.Join(binDir, BinaryName(tag))
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	url := r.DownloadURL + "/" + tag + "/" + AssetName()
	resp, err := r.get(ctx, url, "application/octet-stream")
	if err != nil {
		return "", fmt.Errorf("download Mailpit %s: %w", tag, err)
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(binDir, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := extractBinary(io.LimitReader(resp.Body, 300<<20), tmp); err != nil {
		tmp.Close()
		return "", fmt.Errorf("download Mailpit %s: %w", tag, err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	v, err := BinaryVersion(ctx, tmp.Name())
	if err != nil {
		return "", fmt.Errorf("downloaded binary does not run: %w", err)
	}
	if v != tag {
		return "", fmt.Errorf("downloaded binary reports version %s, expected %s", v, tag)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
}

// extractBinary copies the "mailpit" file from a .tar.gz stream to w.
func extractBinary(r io.Reader, w io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("archive has no mailpit binary")
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == "mailpit" {
			_, err := io.Copy(w, io.LimitReader(tr, 300<<20))
			return err
		}
	}
}

// BinaryVersion runs "<path> version" and returns the version it prints.
func BinaryVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version", "--no-release-check").CombinedOutput()
	if v := parseVersion(out); v != "" {
		return v, nil
	}
	if err != nil {
		// older releases may not know --no-release-check
		out, _ = exec.CommandContext(ctx, bin, "version").CombinedOutput()
		if v := parseVersion(out); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("%s version: %v", bin, err)
	}
	return "", fmt.Errorf("%s does not look like Mailpit", bin)
}

// parseVersion reads "<argv0> v1.28.0 compiled with ..." from the first line.
func parseVersion(out []byte) string {
	line, _, _ := strings.Cut(string(out), "\n")
	f := strings.Fields(line)
	if len(f) >= 3 && f[2] == "compiled" {
		return f[1]
	}
	return ""
}
