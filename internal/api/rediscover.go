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

// Rediscover re-derives the API base URL and auth key from the public SPA bundle.
//
// The key is a literal in the JavaScript, so it can rotate without notice. Refresh calls
// this on a 401/403 rather than failing outright.
func Rediscover(ctx context.Context, httpClient *http.Client) (*Rediscovered, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	page, err := fetchText(ctx, httpClient, SiteURL+"/Interoperability")
	if err != nil {
		return nil, fmt.Errorf("fetching interop SPA: %w", err)
	}
	bundleMatch := bundleRe.FindStringSubmatch(page)
	if bundleMatch == nil {
		return nil, fmt.Errorf("could not find the main.<hash>.js bundle reference in %s/Interoperability", SiteURL)
	}
	bundleName := bundleMatch[1]

	bundle, err := fetchText(ctx, httpClient, SiteURL+"/"+bundleName)
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
