// Package update checks GitHub for newer grove releases (SPEC FR-23) and
// replaces the running binary with the latest one (FR-24).
//
// Everything that comes from the network is untrusted: tags must match
// ^v\d+\.\d+\.\d+$, download URLs (and every redirect hop) must be https on
// a GitHub host, sizes are capped, and archives are verified against
// checksums.txt before anything is extracted.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Repo is the GitHub repository releases are published to.
const Repo = "RafaelXokito/grove"

// DefaultAPIBase is the GitHub REST API root.
const DefaultAPIBase = "https://api.github.com"

// DefaultHosts are the hosts release downloads may come from (including
// redirect targets).
var DefaultHosts = []string{
	"github.com",
	"api.github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
}

// Size caps for network reads.
const (
	maxAPIBody      = 1 << 20   // releases/latest JSON
	maxChecksums    = 1 << 20   // checksums.txt
	maxArchive      = 100 << 20 // grove_*.tar.gz
	maxBinary       = 200 << 20 // the extracted grove binary
	checksumsName   = "checksums.txt"
	binaryEntryName = "grove"
)

var tagRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// ValidTag reports whether s is a plain release tag vX.Y.Z.
func ValidTag(s string) bool { return tagRE.MatchString(s) }

// IsDev reports whether v is a development build (no release version):
// "dev", "(devel)", "", pseudo-versions, anything not vX.Y.Z.
func IsDev(v string) bool { return !ValidTag(v) }

// Compare compares two valid tags numerically: -1, 0 or +1. Invalid tags
// compare as lower than any valid one.
func Compare(a, b string) int {
	pa, okA := parse(a)
	pb, okB := parse(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Newer reports whether latest is a valid tag strictly greater than the
// valid tag current.
func Newer(latest, current string) bool {
	return ValidTag(latest) && ValidTag(current) && Compare(latest, current) > 0
}

func parse(s string) ([3]uint64, bool) {
	var out [3]uint64
	m := tagRE.FindStringSubmatch(s)
	if m == nil {
		return out, false
	}
	for i := range out {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Release is the subset of a GitHub release grove uses.
type Release struct {
	Tag    string
	Assets []Asset
}

// Asset is a downloadable release file.
type Asset struct {
	Name string
	URL  string // browser_download_url
}

// Client talks to the GitHub API and downloads release assets.
type Client struct {
	// APIBase defaults to DefaultAPIBase; tests point it at httptest.
	APIBase string
	// HTTP defaults to http.DefaultClient (timeouts come from contexts).
	HTTP *http.Client
	// Version is sent in the User-Agent.
	Version string
	// Hosts are the allowed download hosts; nil means DefaultHosts.
	Hosts []string
}

func (c *Client) base() string {
	if c.APIBase == "" {
		return DefaultAPIBase
	}
	return strings.TrimRight(c.APIBase, "/")
}

func (c *Client) userAgent() string {
	v := c.Version
	if v == "" {
		v = "dev"
	}
	return "grove/" + v
}

// Latest fetches the latest published (non-draft, non-prerelease) release.
// The tag is validated; a malformed tag is an error.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	u := c.base() + "/repos/" + Repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", c.userAgent())
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("releases/latest: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIBody)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("releases/latest: %w", err)
	}
	if body.Draft || body.Prerelease {
		return Release{}, errors.New("releases/latest: draft or prerelease")
	}
	if !ValidTag(body.TagName) {
		return Release{}, fmt.Errorf("releases/latest: unexpected tag %q", sanitizeTag(body.TagName))
	}
	r := Release{Tag: body.TagName}
	for _, a := range body.Assets {
		r.Assets = append(r.Assets, Asset{Name: a.Name, URL: a.URL})
	}
	return r, nil
}

// sanitizeTag makes a network string safe to print in an error.
func sanitizeTag(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return strconv.QuoteToASCII(s)[1 : len(strconv.QuoteToASCII(s))-1]
}

// checkURL enforces https on an allowed host.
func (c *Client) checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-https download URL %s", u.Redacted())
	}
	hosts := c.Hosts
	if hosts == nil {
		hosts = DefaultHosts
	}
	h := strings.ToLower(u.Hostname())
	for _, ok := range hosts {
		if h == ok {
			return nil
		}
	}
	return fmt.Errorf("refusing download from unexpected host %q", h)
}

// download GETs rawURL (and redirects) only from allowed hosts, reading at
// most max bytes.
func (c *Client) download(ctx context.Context, rawURL string, max int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("download URL: %w", err)
	}
	if err := c.checkURL(u); err != nil {
		return nil, err
	}
	var hc http.Client
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return c.checkURL(req.URL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", u.Redacted(), resp.StatusCode)
	}
	if resp.ContentLength > max {
		return nil, fmt.Errorf("download %s: too large (%d bytes)", u.Redacted(), resp.ContentLength)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("download %s: larger than %d bytes", u.Redacted(), max)
	}
	return data, nil
}
