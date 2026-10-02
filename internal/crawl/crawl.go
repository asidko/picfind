package crawl

import (
	"context"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

type Collection struct {
	Images     map[string]string
	PagesTried int
	PagesOK    int
	PagesLeft  int
	onImage    func(image, page string)
}

func (c *Client) newCollection() *Collection {
	return &Collection{Images: map[string]string{}, onImage: c.OnImage}
}

func (col *Collection) add(r pageResult) {
	col.PagesTried++
	if r.err != nil {
		return
	}
	col.PagesOK++
	for _, img := range r.images {
		if _, ok := col.Images[img]; !ok {
			col.Images[img] = r.url
			if col.onImage != nil {
				col.onImage(img, r.url)
			}
		}
	}
}

type pageResult struct {
	url           string
	final         *url.URL
	links, images []string
	err           error
}

func parallel(ctx context.Context, n, workers int, fn func(i int)) {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, n) {
		wg.Go(func() {
			for i := range jobs {
				fn(i)
			}
		})
	}
feed:
	for i := range n {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
}

func (c *Client) fetchPages(ctx context.Context, urls []string, workers int, fetched *atomic.Int64, onPage func(pageResult)) {
	var mu sync.Mutex
	parallel(ctx, len(urls), workers, func(i int) {
		r := c.page(ctx, urls[i])
		if ctx.Err() != nil {
			return
		}
		if r.err != nil {
			c.debugf("page %s: %v", r.url, r.err)
		}
		mu.Lock()
		onPage(r)
		mu.Unlock()
		if n := fetched.Add(1); n%50 == 0 {
			c.logf("pages fetched: %d", n)
		}
	})
}

type CrawlOptions struct {
	MaxPages  int
	AllHosts  bool
	StayUnder bool
	Pages     *regexp.Regexp
	Workers   int
}

type scope struct{ path, dir string }

func scopeOf(u *url.URL) scope {
	p := u.Path
	if p == "" {
		p = "/"
	}
	cut := strings.LastIndex(p, "/") + 1
	switch {
	case cut == len(p):
		return scope{p, p}
	case slices.Contains(pageExts, strings.ToLower(path.Ext(p[cut:]))):
		return scope{p, p[:cut]}
	}
	return scope{p, p + "/"}
}

func (s scope) contains(u *url.URL) bool {
	p := u.Path
	if p == "" {
		p = "/"
	}
	return p == s.path || strings.HasPrefix(p, s.dir)
}

func (c *Client) Crawl(ctx context.Context, start string, o CrawlOptions) *Collection {
	col := c.newCollection()
	startURL, err := url.Parse(start)
	if err != nil {
		return col
	}
	first := withoutFragment(startURL).String()
	hosts := map[string]bool{strings.ToLower(startURL.Host): true}
	scopes := []scope{scopeOf(startURL)}
	seen := map[string]bool{pageKey(startURL): true}
	frontier := []string{first}
	var fetched atomic.Int64
	for len(frontier) > 0 && ctx.Err() == nil {
		n := len(frontier)
		if o.MaxPages > 0 {
			n = min(n, o.MaxPages-col.PagesTried)
		}
		if n <= 0 {
			break
		}
		batch := frontier[:n]
		var next []string
		c.fetchPages(ctx, batch, o.Workers, &fetched, func(r pageResult) {
			col.add(r)
			if r.final != nil {
				seen[pageKey(r.final)] = true
				if r.url == first {
					hosts[strings.ToLower(r.final.Host)] = true
					scopes = append(scopes, scopeOf(r.final))
				}
			}
			for _, link := range r.links {
				u, err := url.Parse(link)
				if err != nil {
					continue
				}
				key := pageKey(u)
				if seen[key] || !followable(u) || (!o.AllHosts && !hosts[strings.ToLower(u.Host)]) {
					continue
				}
				if o.StayUnder && !slices.ContainsFunc(scopes, func(s scope) bool { return s.contains(u) }) {
					continue
				}
				if o.Pages == nil && isImage(u) || o.Pages != nil && !o.Pages.MatchString(link) {
					continue
				}
				seen[key] = true
				next = append(next, link)
			}
		})
		frontier = append(frontier[n:], next...)
		c.logf("pages crawled: %d, queued: %d, images found: %d", col.PagesTried, len(frontier), len(col.Images))
	}
	if ctx.Err() == nil {
		col.PagesLeft = len(frontier)
	}
	return col
}

func (c *Client) List(ctx context.Context, urls []string, follow *regexp.Regexp, workers int) *Collection {
	col := c.newCollection()
	seen := map[string]bool{}
	for _, u := range urls {
		if p, err := url.Parse(u); err == nil {
			seen[pageKey(p)] = true
		}
	}
	var fetched atomic.Int64
	var next []string
	c.fetchPages(ctx, urls, workers, &fetched, func(r pageResult) {
		col.add(r)
		if follow == nil {
			return
		}
		for _, link := range r.links {
			if !follow.MatchString(link) {
				continue
			}
			u, err := url.Parse(link)
			if err != nil {
				continue
			}
			if key := pageKey(u); !seen[key] {
				seen[key] = true
				next = append(next, link)
			}
		}
	})
	c.logf("listed pages fetched: %d, images found: %d, links to follow: %d", col.PagesTried, len(col.Images), len(next))
	if len(next) == 0 || ctx.Err() != nil {
		return col
	}
	slices.Sort(next)
	c.fetchPages(ctx, next, workers, &fetched, col.add)
	c.logf("followed pages fetched: %d, images found: %d", len(next), len(col.Images))
	return col
}
