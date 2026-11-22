package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// maxDocument caps the release document and SHA256SUMS (a few KB each).
	maxDocument = 256 << 10
	// maxArchive caps a downloaded archive, whatever the document says.
	maxArchive   = 512 << 20
	checkTimeout = 20 * time.Second
	// DefaultStall ends a download that received nothing for this long: a
	// connection that died while the computer slept or the network changed
	// would otherwise hang until TCP gives up (minutes, or never).
	DefaultStall = 60 * time.Second
)

// Client reads releases and downloads assets.
type Client struct {
	HTTP      *http.Client
	LatestURL string // LatestURL in production
	Prefix    string // AssetPrefix in production
	UserAgent string // GitHub's API wants one; a product name, nothing about the hub
	// Stall: a download that receives no byte for this long fails with
	// ErrStalled (0 = DefaultStall).
	Stall time.Duration
}

// NewClient returns the production client. version goes into the User-Agent.
func NewClient(version string) *Client {
	return &Client{
		HTTP: &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) > 5 {
				return errors.New("redirect refused")
			}
			return nil
		}},
		LatestURL: LatestURL,
		Prefix:    AssetPrefix,
		UserAgent: "CrossStream-hub/" + version,
	}
}

// Latest returns the latest release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	doc, err := c.get(ctx, c.LatestURL, maxDocument, "application/vnd.github+json")
	var se *statusError
	if errors.As(err, &se) {
		switch se.code {
		case http.StatusNotFound:
			return Release{}, ErrNoRelease
		case http.StatusForbidden, http.StatusTooManyRequests:
			return Release{}, fmt.Errorf("%w: %w", ErrRateLimited, err)
		}
	}
	if err != nil {
		return Release{}, err
	}
	return Parse(doc, c.Prefix)
}

// Sum returns the published SHA-256 of the asset called name.
func (c *Client) Sum(ctx context.Context, r Release, name string) (string, error) {
	a, ok := r.Asset(SumsName)
	if !ok {
		return "", fmt.Errorf("release %s has no %s", r.Tag, SumsName)
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	sums, err := c.get(ctx, a.URL, maxDocument, "")
	if err != nil {
		return "", err
	}
	return ParseSums(sums, name)
}

func (c *Client) get(ctx context.Context, url string, limit int64, accept string) ([]byte, error) {
	resp, err := c.do(ctx, url, accept)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return b, nil
}

func (c *Client) do(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, &statusError{url: url, code: resp.StatusCode}
	}
	return resp, nil
}

// ErrNoRelease means the repository has no published release yet.
var ErrNoRelease = errors.New("no release published yet")

// ErrRateLimited means GitHub refused the anonymous request (60 an hour per
// address; a hub behind a shared address can run out).
var ErrRateLimited = errors.New("GitHub rate limit")

// ErrStalled means a download received nothing for Client.Stall.
var ErrStalled = errors.New("download stalled")

type statusError struct {
	url  string
	code int
}

func (e *statusError) Error() string { return fmt.Sprintf("get %s: HTTP %d", e.url, e.code) }

// Progress reports bytes downloaded so far and the expected total.
type Progress func(done, total int64)

// Download fetches a into dir (named after the asset), checks its size and
// SHA-256 against want and returns the file path. A file that does not match
// is deleted.
func (c *Client) Download(ctx context.Context, a Asset, want, dir string, progress Progress) (string, error) {
	if !strings.HasPrefix(a.URL, c.Prefix) {
		return "", fmt.Errorf("asset %s: not from %s", a.Name, c.Prefix)
	}
	if a.Size > maxArchive {
		return "", fmt.Errorf("asset %s: %d bytes is too large", a.Name, a.Size)
	}
	stall := c.Stall
	if stall <= 0 {
		stall = DefaultStall
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watchdog := time.AfterFunc(stall, func() { cancel(ErrStalled) })
	defer watchdog.Stop()
	resp, err := c.do(ctx, a.URL, "")
	if err != nil {
		return "", stalled(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	path := filepath.Join(dir, filepath.Base(a.Name))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	h := sha256.New()
	body := &countReader{r: io.LimitReader(resp.Body, maxArchive+1), total: a.Size, progress: progress,
		alive: func() { watchdog.Reset(stall) }}
	n, err := io.Copy(io.MultiWriter(f, h), body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		err = stalled(ctx, fmt.Errorf("download %s: %w", a.Name, err))
	case n > maxArchive || (a.Size > 0 && n != a.Size):
		err = fmt.Errorf("download %s: got %d bytes, want %d", a.Name, n, a.Size)
	case hex.EncodeToString(h.Sum(nil)) != want:
		err = fmt.Errorf("download %s: SHA-256 does not match %s", a.Name, SumsName)
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// stalled marks err with ErrStalled when the watchdog ended the download.
func stalled(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), ErrStalled) && !errors.Is(err, ErrStalled) {
		return fmt.Errorf("%w: %w", ErrStalled, err)
	}
	return err
}

type countReader struct {
	r        io.Reader
	done     int64
	total    int64
	progress Progress
	alive    func() // a byte arrived (the stall watchdog)
	last     time.Time
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	if n > 0 && c.alive != nil {
		c.alive()
	}
	if c.progress != nil && (err != nil || time.Since(c.last) > 200*time.Millisecond) {
		c.last = time.Now()
		c.progress(c.done, c.total)
	}
	return n, err
}

// FileSum returns the SHA-256 (lowercase hex) of the file at path.
func FileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
