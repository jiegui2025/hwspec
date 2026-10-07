package fwindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// The only file in hwspec besides internal/ids/sync.go that reaches the
// network (depguard no-network; ADR 0012), and only through
// http.DefaultTransport (TestRequestsOnlyGoThroughTheDefaultTransport).

type response struct {
	body        []byte
	etag        string
	notModified bool
}

// getter fetches url, at most limit bytes; with an ETag it asks only for a
// changed copy.
type getter func(ctx context.Context, url, etag string, limit int64) (*response, error)

func httpGetter(userAgent string) getter {
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: sameHost}
	return func(ctx context.Context, url, etag string, limit int64) (*response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusNotModified:
			if etag == "" {
				return nil, fmt.Errorf("%s: HTTP %d to a request for the whole file", url, resp.StatusCode)
			}
			return &response{notModified: true, etag: etag}, nil
		case http.StatusOK:
		default:
			return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode) // the reason phrase is the server's text: not shown
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", url, err)
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("%s: larger than %d bytes", url, limit)
		}
		return &response{body: b, etag: resp.Header.Get("ETag")}, nil
	}
}

// sameHost follows a redirect only within the host and scheme first asked.
func sameHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if first := via[0].URL; req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return fmt.Errorf("redirected to another host (%s://%s)", req.URL.Scheme, req.URL.Host)
	}
	return nil
}
