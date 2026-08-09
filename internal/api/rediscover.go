package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

var (
	bundleRe  = regexp.MustCompile(`src="(main\.[0-9a-f]+\.js)"`)
	authKeyRe = regexp.MustCompile(`"X-Auth-Key":"([^"]+)"`)
	// The bundle contains both a production and an "esp-stg" staging URL. Match only
	// the production host.
	serviceRe = regexp.MustCompile(`simServiceUrl:"(https://interop\.esp\.[^"]+)"`)
)

// Rediscovered is a base URL and auth key re-derived from the live SPA bundle.
type Rediscovered struct {
	Base    string
	AuthKey string
	Bundle  string
}

// Rediscover derives the API base URL and auth key from the public SPA bundle.
//
// This is the only source of the key: nothing is compiled in and nothing is cached to
// disk, so every process starts here and a rotation between runs is invisible.
func Rediscover(ctx context.Context, httpClient *http.Client) (*Rediscovered, error) {
	return rediscoverFrom(ctx, httpClient, SiteURL)
}

// rediscoverFrom is Rediscover against an arbitrary origin, so tests can serve a fixture
// bundle instead of reaching the live site.
func rediscoverFrom(ctx context.Context, httpClient *http.Client, site string) (*Rediscovered, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	page, err := fetchText(ctx, httpClient, site+"/Interoperability")
	if err != nil {
		return nil, fmt.Errorf("fetching interop SPA: %w", err)
	}
	bundleMatch := bundleRe.FindStringSubmatch(page)
	if bundleMatch == nil {
		return nil, fmt.Errorf("could not find the main.<hash>.js bundle reference in %s/Interoperability", site)
	}
	bundleName := bundleMatch[1]

	bundle, err := fetchText(ctx, httpClient, site+"/"+bundleName)
	if err != nil {
		return nil, fmt.Errorf("fetching bundle %s: %w", bundleName, err)
	}

	keyMatch := authKeyRe.FindStringSubmatch(bundle)
	if keyMatch == nil {
		return nil, fmt.Errorf("could not find an X-Auth-Key literal in %s", bundleName)
	}
	out := &Rediscovered{AuthKey: keyMatch[1], Bundle: bundleName, Base: DefaultBase}
	if svc := serviceRe.FindStringSubmatch(bundle); svc != nil {
		out.Base = svc[1]
	}
	return out, nil
}

func fetchText(ctx context.Context, c *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("got HTTP %d", resp.StatusCode)
	}
	// The bundle is ~5.5 MB; cap generously to avoid an unbounded read.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}
