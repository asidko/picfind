package crawl

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asidko/picfind/internal/ahash"
)

func pngBytes(t *testing.T, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for y := range 30 {
		for x := range 40 {
			img.Set(x, y, color.RGBA{uint8((x*seed + y*7) % 256), uint8((y * seed) % 256), uint8(seed * 30), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func hashOf(t *testing.T, b []byte) ahash.Hash {
	t.Helper()
	h, err := ahash.FromReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestExtract(t *testing.T) {
	base, _ := url.Parse("https://example.com/dir/page.html")
	doc := `<html><head>
		<meta property="og:image" content="/og.jpg">
		<meta name="twitter:image" content="https://cdn.example.com/tw.jpg">
		<link rel="shortcut icon" href="/favicon.ico">
		<link rel="stylesheet" href="/style.css">
	</head><body>
		<a href="next.html#top">next</a>
		<a href="/about/">about</a>
		<a href="/go?to=/">go</a>
		<a href="javascript:void(0)">js</a>
		<a href="mailto:a@example.com">mail</a>
		<a href="#section">anchor</a>
		<img src="/assets/loading.svg" data-src="lazy.jpg">
		<img srcset="small.jpg 320w, big.jpg 1280w">
		<picture><source srcset="pic.webp 1x"></picture>
		<img src="data:image/png;base64,AAAA">
	</body></html>`
	links, images := extract(strings.NewReader(doc), base)
	wantLinks := []string{"https://example.com/dir/next.html", "https://example.com/about/", "https://example.com/go?to=/"}
	wantImages := []string{
		"https://example.com/og.jpg", "https://cdn.example.com/tw.jpg", "https://example.com/dir/lazy.jpg",
		"https://example.com/dir/small.jpg", "https://example.com/dir/big.jpg", "https://example.com/dir/pic.webp",
	}
	if !slices.Equal(links, wantLinks) {
		t.Errorf("links\n got %v\nwant %v", links, wantLinks)
	}
	if !slices.Equal(images, wantImages) {
		t.Errorf("images\n got %v\nwant %v", images, wantImages)
	}
}

func TestExtractSrcset(t *testing.T) {
	base, _ := url.Parse("https://example.com/")
	doc := `<img srcset="data:image/gif;base64,R0lGODlhAQABAAAAACw= 1x, /c/w_400,h_300/a.jpg 400w,/c/w_800,h_600/a.jpg 800w">
		<picture><source srcset="  /b.jpg,/d.jpg  ,
			/e.jpg 2x , /f.jpg"></picture>`
	_, images := extract(strings.NewReader(doc), base)
	want := []string{
		"https://example.com/c/w_400,h_300/a.jpg", "https://example.com/c/w_800,h_600/a.jpg",
		"https://example.com/b.jpg,/d.jpg", "https://example.com/e.jpg", "https://example.com/f.jpg",
	}
	if !slices.Equal(images, want) {
		t.Errorf("images\n got %v\nwant %v", images, want)
	}
}

func TestExtractSkipsUnhashable(t *testing.T) {
	base, _ := url.Parse("https://example.com/")
	doc := `<link rel="icon" href="/icon.png">
		<link rel="apple-touch-icon" href="/touch.png">
		<meta property="og:image" content="/og.svg">
		<img src="/logo.SVG">
		<img src="/favicon.Ico?v=2">
		<img srcset="/a.svg 1x, /photo.jpg 2x">`
	_, images := extract(strings.NewReader(doc), base)
	if !slices.Equal(images, []string{"https://example.com/photo.jpg"}) {
		t.Errorf("got %v", images)
	}
}

func TestExtractNoscriptAndImageLinks(t *testing.T) {
	base, _ := url.Parse("https://example.com/g/")
	doc := `<img class="lazy" data-lazy="real.jpg"><noscript><img src="real.jpg"><a href="/next">n</a></noscript>
		<a href="/full/photo.JPG"><img src="/thumb/photo.jpg"></a><a href="/album/2">album</a>`
	links, images := extract(strings.NewReader(doc), base)
	wantImages := []string{"https://example.com/g/real.jpg", "https://example.com/full/photo.JPG", "https://example.com/thumb/photo.jpg"}
	wantLinks := []string{"https://example.com/next", "https://example.com/full/photo.JPG", "https://example.com/album/2"}
	if !slices.Equal(images, wantImages) {
		t.Errorf("images\n got %v\nwant %v", images, wantImages)
	}
	if !slices.Equal(links, wantLinks) {
		t.Errorf("links\n got %v\nwant %v", links, wantLinks)
	}
}

func TestExtractBaseTag(t *testing.T) {
	base, _ := url.Parse("https://example.com/a/b.html")
	_, images := extract(strings.NewReader(`<base href="https://static.example.com/x/"><img src="y.png">`), base)
	if !slices.Equal(images, []string{"https://static.example.com/x/y.png"}) {
		t.Errorf("got %v", images)
	}
}

func site(t *testing.T, target []byte) *httptest.Server {
	t.Helper()
	other := pngBytes(t, 3)
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/{$}", page(`<a href="/a">a</a> <a href="/doc.pdf">pdf</a> <a href="https://elsewhere.invalid/x">x</a> <img src="/img/other.png">`))
	mux.HandleFunc("/a", page(`<a href="/b">b</a> <img data-src="/img/target.png">`))
	mux.HandleFunc("/b", page(`<a href="/">home</a> <img src="/img/other.png">`))
	mux.HandleFunc("/list-1", page(`<a href="/thread-7">t</a> <a href="/about">about</a>`))
	mux.HandleFunc("/thread-7", page(`<img src="/img/target.png">`))
	mux.HandleFunc("/img/target.png", func(w http.ResponseWriter, r *http.Request) { w.Write(target) })
	mux.HandleFunc("/img/other.png", func(w http.ResponseWriter, r *http.Request) { w.Write(other) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCrawlFindsImage(t *testing.T) {
	target := pngBytes(t, 11)
	srv := site(t, target)
	c := NewClient(4)
	var live []Result
	var mu sync.Mutex
	h := c.NewHasher(context.Background(), hashOf(t, target), 4, func(r Result) {
		mu.Lock()
		live = append(live, r)
		mu.Unlock()
	})
	c.OnImage = func(image, page string) { h.Add(image, page) }
	col := c.Crawl(context.Background(), srv.URL+"/", CrawlOptions{Workers: 4})
	if col.PagesOK != 3 || col.PagesTried != 3 {
		t.Fatalf("pages ok=%d tried=%d, want 3/3", col.PagesOK, col.PagesTried)
	}
	if got := col.Images[srv.URL+"/img/target.png"]; got != srv.URL+"/a" {
		t.Fatalf("target image found on %q, want /a", got)
	}
	results, failed := h.Wait()
	if failed != 0 || len(results) != 2 || len(live) != 2 {
		t.Fatalf("results=%d live=%d failed=%d", len(results), len(live), failed)
	}
	if results[0].Distance != 0 || results[0].Image != srv.URL+"/img/target.png" || results[0].Page != srv.URL+"/a" {
		t.Errorf("best result %+v", results[0])
	}
}

func TestHasherStopsOnCancel(t *testing.T) {
	srv := site(t, pngBytes(t, 11))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := NewClient(1).NewHasher(ctx, 0, 1, nil)
	h.Add(srv.URL+"/img/other.png", srv.URL)
	if results, failed := h.Wait(); len(results) != 0 || failed != 0 {
		t.Errorf("cancelled hasher produced results=%d failed=%d", len(results), failed)
	}
}

func TestCrawlMaxPages(t *testing.T) {
	srv := site(t, pngBytes(t, 11))
	col := NewClient(2).Crawl(context.Background(), srv.URL, CrawlOptions{MaxPages: 2, Workers: 2})
	if col.PagesTried != 2 {
		t.Errorf("tried %d pages, want 2", col.PagesTried)
	}
}

func TestCrawlFollowsStartRedirectToOtherHost(t *testing.T) {
	dst := site(t, pngBytes(t, 11))
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL+r.URL.RequestURI(), http.StatusMovedPermanently)
	}))
	defer src.Close()
	col := NewClient(2).Crawl(context.Background(), src.URL+"/", CrawlOptions{Workers: 2})
	if col.PagesOK != 3 || col.PagesTried != 3 {
		t.Fatalf("pages ok=%d tried=%d, want 3/3", col.PagesOK, col.PagesTried)
	}
	if got := col.Images[dst.URL+"/img/target.png"]; got != dst.URL+"/a" {
		t.Errorf("target image found on %q, want %s/a", got, dst.URL)
	}
}

func TestCrawlFollowPatternGoesDeep(t *testing.T) {
	target := pngBytes(t, 11)
	var otherHits atomic.Int32
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/{$}", page(`<a href="/gallery/a">a</a> <a href="/other/x">x</a>`))
	mux.HandleFunc("/gallery/a", page(`<a href="/gallery/b">b</a>`))
	mux.HandleFunc("/gallery/b", page(`<a href="/gallery/c">c</a>`))
	mux.HandleFunc("/gallery/c", page(`<img src="/img/target.png">`))
	mux.HandleFunc("/other/x", func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		page(`<a href="/gallery/a">a</a>`)(w, r)
	})
	mux.HandleFunc("/img/target.png", func(w http.ResponseWriter, r *http.Request) { w.Write(target) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	follow := regexp.MustCompile(`^` + regexp.QuoteMeta(srv.URL) + `/gallery/`)
	col := NewClient(2).Crawl(context.Background(), srv.URL+"/", CrawlOptions{Pages: follow, Workers: 2})
	if got := col.Images[srv.URL+"/img/target.png"]; got != srv.URL+"/gallery/c" {
		t.Errorf("target image found on %q, want /gallery/c", got)
	}
	if col.PagesTried != 4 {
		t.Errorf("tried %d pages, want 4 (start + 3 gallery pages)", col.PagesTried)
	}
	if n := otherHits.Load(); n != 0 {
		t.Errorf("/other/x fetched %d times, want 0", n)
	}
}

func TestImageLinksVisitedOnlyWhenRegexMatches(t *testing.T) {
	hungUp := make(chan bool, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/v/abc.jpg"><img src="/thumb.png"></a> <a href="/real.png">r</a>`)
	})
	mux.HandleFunc("/v/abc.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<img src="/full.png">`)
	})
	mux.HandleFunc("/real.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			hungUp <- true
		case <-time.After(5 * time.Second):
			hungUp <- false
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cases := []struct {
		name      string
		pages     *regexp.Regexp
		tried     int
		fullFound bool
	}{
		{"no regex: image links are only hashed", nil, 1, false},
		{"matching regex: viewer page is visited", regexp.MustCompile(`/v/`), 2, true},
		{"regex matching a real image: page fetch stops at the header", regexp.MustCompile(`\.(jpg|png)$`), 3, true},
	}
	for _, tc := range cases {
		col := NewClient(2).Crawl(context.Background(), srv.URL+"/", CrawlOptions{Pages: tc.pages, Workers: 2})
		_, fullFound := col.Images[srv.URL+"/full.png"]
		_, viewerHashed := col.Images[srv.URL+"/v/abc.jpg"]
		if col.PagesTried != tc.tried || fullFound != tc.fullFound || !viewerHashed {
			t.Errorf("%s: tried %d pages, full image found %v, viewer URL hashed %v", tc.name, col.PagesTried, fullFound, viewerHashed)
		}
	}
	if !<-hungUp {
		t.Error("page fetch of an image kept reading the body")
	}
}

func TestPageURLsFetchedVerbatimAndDeduped(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="/go?to=/">go</a> <a href="/dir/">d</a> <a href="/dir">d</a> <a href="/dir/#top">d</a> <a href="/">home</a>`)
		case "/dir/":
			fmt.Fprint(w, `<img src="/x.png">`)
		}
	}))
	defer srv.Close()
	requests := func() []string {
		mu.Lock()
		defer mu.Unlock()
		got := slices.Sorted(slices.Values(hits))
		hits = nil
		return got
	}
	want := []string{"/", "/dir/", "/go?to=/"}
	col := NewClient(2).Crawl(context.Background(), srv.URL+"/", CrawlOptions{Workers: 2})
	if got := requests(); !slices.Equal(got, want) {
		t.Errorf("crawl requests %v, want %v", got, want)
	}
	if got := col.Images[srv.URL+"/x.png"]; got != srv.URL+"/dir/" {
		t.Errorf("crawl reported page %q, want %s/dir/", got, srv.URL)
	}
	col = NewClient(2).List(context.Background(), []string{srv.URL + "/"}, regexp.MustCompile(`.`), 2)
	if got := requests(); !slices.Equal(got, want) {
		t.Errorf("list requests %v, want %v", got, want)
	}
	if got := col.Images[srv.URL+"/x.png"]; got != srv.URL+"/dir/" {
		t.Errorf("list reported page %q, want %s/dir/", got, srv.URL)
	}
}

func TestListFollowsPattern(t *testing.T) {
	target := pngBytes(t, 11)
	srv := site(t, target)
	col := NewClient(2).List(context.Background(), []string{srv.URL + "/list-1"}, regexp.MustCompile(`/thread-[0-9]+`), 2)
	if col.PagesTried != 2 {
		t.Errorf("tried %d pages, want 2 (listing + thread)", col.PagesTried)
	}
	if got := col.Images[srv.URL+"/img/target.png"]; got != srv.URL+"/thread-7" {
		t.Errorf("target found on %q", got)
	}
}

func TestRetryOnRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := NewClient(1)
	c.backoff = time.Millisecond
	res, err := c.get(context.Background(), srv.URL, time.Second, 10, false)
	if err != nil || string(res.body) != "ok" || calls.Load() != 3 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestRetryAfterZero(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := NewClient(1)
	c.backoff = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.get(ctx, srv.URL, time.Second, 10, false)
	if err != nil || string(res.body) != "ok" || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestErrorBodyDrainedForReuse(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("x", 8<<10), http.StatusNotFound)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	c := NewClient(1)
	for range 3 {
		if _, err := c.get(context.Background(), srv.URL, time.Second, 10, false); err == nil {
			t.Fatal("404 accepted")
		}
	}
	if n := conns.Load(); n != 1 {
		t.Errorf("opened %d connections for 3 requests, want 1", n)
	}
}

func TestBodyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if r.URL.Query().Has("chunked") {
			w.(http.Flusher).Flush()
		}
		w.Write(make([]byte, n))
	}))
	defer srv.Close()
	c := NewClient(1)
	for _, tc := range []struct {
		query string
		ok    bool
	}{
		{"n=10", true},
		{"n=10&chunked", true},
		{"n=11", false},
		{"n=11&chunked", false},
		{"n=100", false},
	} {
		res, err := c.get(context.Background(), srv.URL+"/?"+tc.query, time.Second, 10, false)
		if tc.ok && (err != nil || len(res.body) != 10) {
			t.Errorf("%s: err=%v, want 10-byte body", tc.query, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: oversized body accepted", tc.query)
		}
	}
}

func TestScope(t *testing.T) {
	cases := []struct {
		start string
		in    []string
		out   []string
	}{
		{"https://x.com/gallery/", []string{"/gallery/", "/gallery/a", "/gallery/a/b.html"}, []string{"/", "/gallery", "/galleryx/a", "/news/"}},
		{"https://x.com/gallery", []string{"/gallery", "/gallery/a"}, []string{"/galleryx", "/"}},
		{"https://x.com/gallery/index.html", []string{"/gallery/2.html", "/gallery/a/"}, []string{"/other/"}},
		{"https://x.com/user/7?tab=photos", []string{"/user/7", "/user/7/photos"}, []string{"/user/8", "/user/"}},
		{"https://x.com", []string{"/", "/anything/deep"}, nil},
		{"https://x.com/user/john.doe", []string{"/user/john.doe", "/user/john.doe/photos"}, []string{"/user/jane", "/user/"}},
		{"https://x.com/forum/list.PHP?page=2", []string{"/forum/topic-1", "/forum/list.PHP"}, []string{"/news"}},
	}
	for _, c := range cases {
		su, _ := url.Parse(c.start)
		s := scopeOf(su)
		for _, p := range c.in {
			if u, _ := url.Parse("https://x.com" + p + "?q=1"); !s.contains(u) {
				t.Errorf("%s: %s should be inside", c.start, p)
			}
		}
		for _, p := range c.out {
			if u, _ := url.Parse("https://x.com" + p); s.contains(u) {
				t.Errorf("%s: %s should be outside", c.start, p)
			}
		}
	}
}

func TestCrawlStayUnder(t *testing.T) {
	var outside atomic.Int32
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/gallery", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/gallery/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/gallery/{$}", page(`<a href="/gallery/a">a</a><a href="/">home</a><a href="/news/1">news</a>`))
	mux.HandleFunc("/gallery/a", page(`<a href="/gallery/a/b">b</a>`))
	mux.HandleFunc("/gallery/a/b", page(`<img src="/img/deep.png">`))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		outside.Add(1)
		page(`<img src="/img/outside.png">`)(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	col := NewClient(2).Crawl(context.Background(), srv.URL+"/gallery", CrawlOptions{StayUnder: true, Workers: 2})
	if _, ok := col.Images[srv.URL+"/img/deep.png"]; !ok {
		t.Errorf("deep image not reached, images: %v", col.Images)
	}
	if n := outside.Load(); n != 0 || col.PagesTried != 3 {
		t.Errorf("pages outside the start path fetched %d times, %d pages tried", n, col.PagesTried)
	}
}

func TestMatchReportedBeforeLevelFinishes(t *testing.T) {
	target := pngBytes(t, 11)
	release := make(chan struct{})
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/{$}", page(`<a href="/fast">f</a><a href="/slow">s</a>`))
	mux.HandleFunc("/fast", page(`<img src="/target.png">`))
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		page(`<p>late</p>`)(w, r)
	})
	mux.HandleFunc("/target.png", func(w http.ResponseWriter, r *http.Request) { w.Write(target) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release)
	c := NewClient(4)
	found := make(chan Result, 1)
	h := c.NewHasher(context.Background(), hashOf(t, target), 2, func(r Result) {
		if r.Distance == 0 {
			found <- r
		}
	})
	c.OnImage = h.Add
	crawled := make(chan *Collection)
	go func() { crawled <- c.Crawl(context.Background(), srv.URL+"/", CrawlOptions{Workers: 4}) }()
	select {
	case r := <-found:
		if r.Page != srv.URL+"/fast" {
			t.Errorf("match on %s", r.Page)
		}
	case <-crawled:
		t.Fatal("crawl finished before the match was reported")
	case <-time.After(5 * time.Second):
		t.Fatal("match not reported while /slow was still loading")
	}
	release <- struct{}{}
	<-crawled
	h.Wait()
}
