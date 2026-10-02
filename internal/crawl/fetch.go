package crawl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	userAgent    = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	pageTimeout  = 15 * time.Second
	imageTimeout = 20 * time.Second
	pageLimit    = 10 << 20
	imageLimit   = 50 << 20
	drainLimit   = 64 << 10
	maxRetries   = 3
	maxRetryWait = 30 * time.Second
)

type Client struct {
	httpClient *http.Client
	backoff    time.Duration
	Logf       func(format string, args ...any)
	Debugf     func(format string, args ...any)
	OnImage    func(image, page string)
}

func NewClient(conns int) *Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = conns * 2
	t.MaxIdleConnsPerHost = conns * 2
	return &Client{httpClient: &http.Client{Transport: t}, backoff: time.Second}
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

func (c *Client) debugf(format string, args ...any) {
	if c.Debugf != nil {
		c.Debugf(format, args...)
	}
}

type response struct {
	body  []byte
	final *url.URL
}

func (c *Client) retryWait(resp *http.Response, attempt int) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, maxRetryWait)
	}
	return min(c.backoff<<attempt, maxRetryWait)
}

func (c *Client) get(ctx context.Context, rawURL string, timeout time.Duration, limit int64, htmlOnly bool) (*response, error) {
	for attempt := 0; ; attempt++ {
		res, wait, retry, err := c.try(ctx, rawURL, timeout, limit, htmlOnly, attempt)
		if !retry || attempt >= maxRetries {
			return res, err
		}
		c.debugf("%s: rate limited, retrying in %s", rawURL, wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *Client) try(ctx context.Context, rawURL string, timeout time.Duration, limit int64, htmlOnly bool, attempt int) (res *response, wait time.Duration, retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.CopyN(io.Discard, resp.Body, drainLimit)
		err = fmt.Errorf("HTTP %d", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			return nil, c.retryWait(resp, attempt), true, err
		}
		return nil, 0, false, err
	}
	if ct := resp.Header.Get("Content-Type"); htmlOnly && !strings.Contains(strings.ToLower(ct), "html") {
		return nil, 0, false, fmt.Errorf("not HTML: %q", ct)
	}
	body, err := readBody(resp, limit)
	if err != nil {
		return nil, 0, false, err
	}
	return &response{body: body, final: resp.Request.URL}, 0, false, nil
}

func readBody(resp *http.Response, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	if n := resp.ContentLength; n > 0 && n <= limit {
		buf.Grow(int(n) + bytes.MinRead)
	}
	if _, err := buf.ReadFrom(io.LimitReader(resp.Body, limit+1)); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > limit {
		return nil, fmt.Errorf("body larger than %d bytes", limit)
	}
	return buf.Bytes(), nil
}

func (c *Client) page(ctx context.Context, rawURL string) pageResult {
	r := pageResult{url: rawURL}
	res, err := c.get(ctx, rawURL, pageTimeout, pageLimit, true)
	if err != nil {
		r.err = err
		return r
	}
	r.final = res.final
	r.links, r.images = extract(bytes.NewReader(res.body), res.final)
	return r
}
