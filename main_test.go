package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testPNG(t *testing.T, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := range 24 {
		for x := range 32 {
			img.Set(x, y, color.RGBA{uint8((x*seed + y*5) % 256), uint8((y * seed * 3) % 256), uint8(seed * 40), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testSite(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	target, other := testPNG(t, 7), testPNG(t, 2)
	mux := http.NewServeMux()
	html := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/{$}", html(`<a href="/p2">2</a><img src="/other.png">`))
	mux.HandleFunc("/p2", html(`<img src="/target.png">`))
	for _, n := range []string{"1", "2", "3"} {
		mux.HandleFunc("/forum-"+n, html(`<a href="/thread-1">t</a>`))
	}
	mux.HandleFunc("/thread-1", html(`<img src="/target.png">`))
	mux.HandleFunc("/broken", html(`<img src="/broken.png">`))
	mux.HandleFunc("/target.png", func(w http.ResponseWriter, r *http.Request) { w.Write(target) })
	mux.HandleFunc("/other.png", func(w http.ResponseWriter, r *http.Request) { w.Write(other) })
	mux.HandleFunc("/broken.png", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "not an image") })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	file := filepath.Join(t.TempDir(), "target.png")
	if err := os.WriteFile(file, target, 0o644); err != nil {
		t.Fatal(err)
	}
	return srv, file
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func imageHash(t *testing.T, file string) string {
	t.Helper()
	code, out, errOut := runCLI(t, "--image-file", file)
	if code != exitOK {
		t.Fatalf("hash-only exit %d: %s", code, errOut)
	}
	return strings.TrimSpace(out)
}

func TestHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}} {
		code, out, _ := runCLI(t, args...)
		if code != exitOK || !strings.HasPrefix(out, "picfind dev (") || !strings.Contains(out, "\nexamples:\n") {
			t.Errorf("%v: exit %d, output %q", args, code, out)
		}
		for _, section := range []string{"\nwhat to look for:\n", "\npages to visit:\n", "\nimages to hash:\n", "\noutput and speed:\n"} {
			if !strings.Contains(out, section) {
				t.Errorf("%v: help has no %q section", args, strings.TrimSpace(section))
			}
		}
		if !strings.Contains(out, "\n  --visit-images-regex <regex>   only hash images whose URL matches regex\n") {
			t.Errorf("%v: --visit-images-regex help line changed:\n%s", args, out)
		}
	}
	code, out, _ := runCLI(t, "--version")
	if code != exitOK || !strings.HasPrefix(out, "picfind dev (") || strings.Count(out, "\n") != 1 {
		t.Errorf("--version: exit %d, output %q", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	srv, file := testSite(t)
	hash := "3c7e7e3c18183c7e"
	tmpl := srv.URL + "/forum-{n}"
	list := writeFile(t, "pages.txt", srv.URL+"/forum-1\n")
	badList := writeFile(t, "pages.txt", "# listing\n"+srv.URL+"/forum-1\n\nfoo\n")
	ftpList := writeFile(t, "pages.txt", "ftp://example.com/a\n")
	cases := map[string]struct {
		args []string
		want string
	}{
		"unknown flag":             {[]string{"--nope"}, "unknown option: -nope"},
		"bad hash":                 {[]string{srv.URL, "xyz"}, `"xyz" is neither a 16/32-hex hash nor an http(s):// URL`},
		"short hash":               {[]string{srv.URL, "3c7e7e3c1818"}, `"3c7e7e3c1818" is neither`},
		"host without scheme":      {[]string{"example.com", hash}, `"example.com" is neither a 16/32-hex hash nor an http(s):// URL`},
		"url without host":         {[]string{"http://", hash}, "not an http(s) URL: http://"},
		"url with bad host":        {[]string{"https://exa mple.com", hash}, "not an http(s) URL: https://exa mple.com"},
		"no hash":                  {[]string{srv.URL}, "missing <hash> or --image-file"},
		"hash and file":            {[]string{srv.URL, hash, "--image-file", file}, "give either <hash> or --image-file"},
		"hash without url":         {[]string{hash}, "missing <url> or --urls-file"},
		"two urls":                 {[]string{srv.URL, srv.URL, hash}, "unexpected argument: " + srv.URL},
		"two hashes":               {[]string{srv.URL, hash, hash}, "unexpected argument: " + hash},
		"template no pages":        {[]string{srv.URL, hash, "--page-template", tmpl}, "--page-template and --pages go together"},
		"template no {n}":          {[]string{srv.URL, hash, "--page-template", srv.URL + "/forum", "--pages", "1-2"}, "must contain {n}"},
		"template not http":        {[]string{srv.URL, hash, "--page-template", "ftp://example.com/forum-{n}", "--pages", "1-2"}, "--page-template is not an http(s) URL: ftp://example.com/forum-1"},
		"template without host":    {[]string{srv.URL, hash, "--page-template", "https:///forum-{n}", "--pages", "1-2"}, "--page-template is not an http(s) URL: https:///forum-1"},
		"bad range":                {[]string{srv.URL, hash, "--page-template", tmpl, "--pages", "5-2"}, "--pages must be START-END"},
		"huge range":               {[]string{srv.URL, hash, "--page-template", tmpl, "--pages", "0-9223372036854775807"}, "--pages may cover at most 1000000 pages"},
		"range one over limit":     {[]string{srv.URL, hash, "--page-template", tmpl, "--pages", "1-1000001"}, "--pages may cover at most 1000000 pages"},
		"bad urls-file line":       {[]string{hash, "--urls-file", badList}, badList + ":4: not an http(s) URL: foo"},
		"ftp in urls-file":         {[]string{hash, "--urls-file", ftpList}, ftpList + ":1: not an http(s) URL: ftp://example.com/a"},
		"max-pages with urls-file": {[]string{hash, "--urls-file", list, "--max-pages", "10"}, "--max-pages, --stay-under and --all-hosts only apply when crawling from <url>"},
		"max-pages default value":  {[]string{hash, "--urls-file", list, "--max-pages", "500"}, "only apply when crawling from <url>"},
		"all-hosts with template":  {[]string{srv.URL, hash, "--page-template", tmpl, "--pages", "2-3", "--all-hosts"}, "only apply when crawling from <url>"},
		"stay-under with list":     {[]string{hash, "--urls-file", list, "--stay-under"}, "only apply when crawling from <url>"},
		"stay-under and all-hosts": {[]string{srv.URL, hash, "--stay-under", "--all-hosts"}, "use either --stay-under or --all-hosts"},
		"bad pages regex":          {[]string{hash, "--urls-file", file, "--visit-pages-regex", "("}, "invalid --visit-pages-regex"},
		"bad images regex":         {[]string{srv.URL, hash, "--visit-images-regex", "["}, "invalid --visit-images-regex"},
		"zero workers":             {[]string{srv.URL, hash, "--workers", "0"}, "--workers and --page-workers must be at least 1"},
		"max-dist too large":       {[]string{srv.URL, hash, "--max-dist", "65"}, "--max-dist must be 0-64"},
	}
	for name, tc := range cases {
		code, _, errOut := runCLI(t, tc.args...)
		if code != exitUsage || !strings.Contains(errOut, "picfind --help") || !strings.Contains(errOut, tc.want) {
			t.Errorf("%s: exit %d, stderr %q, want %q", name, code, errOut, tc.want)
		}
	}
}

func TestExpandPagesLimit(t *testing.T) {
	urls, err := expandPages("https://example.com/p-{n}", "1-1000000")
	if err != nil || len(urls) != maxPageRange || urls[len(urls)-1] != "https://example.com/p-1000000" {
		t.Fatalf("1-1000000: %d urls, err %v", len(urls), err)
	}
	for _, spec := range []string{"0-1000000", "0-9223372036854775807"} {
		if _, err := expandPages("https://example.com/p-{n}", spec); err == nil {
			t.Errorf("%s: no error", spec)
		}
	}
}

func TestHashOnly(t *testing.T) {
	_, file := testSite(t)
	if h := imageHash(t, file); len(h) != 16 {
		t.Errorf("hash %q", h)
	}
	if code, _, _ := runCLI(t, "--image-file", filepath.Join(t.TempDir(), "missing.png")); code != exitRuntime {
		t.Errorf("missing file exit %d, want %d", code, exitRuntime)
	}
}

func TestCrawlMatch(t *testing.T) {
	srv, file := testSite(t)
	hash := imageHash(t, file)
	logLine := regexp.MustCompile(`(?m)^\d{2}:\d{2}:\d{2} target aHash: ` + hash + `$`)
	for _, args := range [][]string{
		{srv.URL, hash},
		{hash, srv.URL},
		{strings.Replace(srv.URL, "http://", "HTTP://", 1), hash},
		{srv.URL, strings.ToUpper(hash) + "5a5aa5a50f0ff0f0"},
		{srv.URL, "--image-file", file},
	} {
		code, out, errOut := runCLI(t, args...)
		if code != exitOK || !strings.Contains(out, "match: distance 0\n  image: "+srv.URL+"/target.png\n  page:  "+srv.URL+"/p2\n") || !strings.Contains(out, "[FOUND] distance 0  image: "+srv.URL+"/target.png  page: "+srv.URL+"/p2\n") {
			t.Errorf("%v: exit %d\nstdout %s\nstderr %s", args, code, out, errOut)
		}
		if !logLine.MatchString(errOut) {
			t.Errorf("%v: no timestamped target log line in stderr %q", args, errOut)
		}
	}
}

func TestVisitPagesRegexCrawl(t *testing.T) {
	target, decoy := testPNG(t, 7), testPNG(t, 2)
	var otherHits atomic.Int64
	mux := http.NewServeMux()
	html := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/{$}", html(`<a href="/gallery/1">g</a><a href="/other/1">o</a>`))
	mux.HandleFunc("/gallery/1", html(`<a href="/gallery/2">next</a><a href="/other/1">o</a>`))
	mux.HandleFunc("/gallery/2", html(`<img src="/target.png">`))
	mux.HandleFunc("/other/1", func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<img src="/decoy.png">`)
	})
	mux.HandleFunc("/target.png", func(w http.ResponseWriter, r *http.Request) { w.Write(target) })
	mux.HandleFunc("/decoy.png", func(w http.ResponseWriter, r *http.Request) { w.Write(decoy) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hash := imageHash(t, writeFile(t, "target.png", string(target)))
	code, out, errOut := runCLI(t, srv.URL, hash, "--visit-pages-regex", "^"+regexp.QuoteMeta(srv.URL)+"/gallery/", "--max-pages", "0")
	if code != exitOK || !strings.Contains(out, "=== 1 images hashed, 0 failed") || !strings.Contains(out, "match: distance 0\n  image: "+srv.URL+"/target.png\n  page:  "+srv.URL+"/gallery/2\n") {
		t.Errorf("exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	if n := otherHits.Load(); n != 0 {
		t.Errorf("/other/1 fetched %d times", n)
	}
}

func TestStayUnder(t *testing.T) {
	target, decoy := testPNG(t, 7), testPNG(t, 2)
	var outside atomic.Int64
	mux := http.NewServeMux()
	html := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/gallery/{$}", html(`<a href="/gallery/1">1</a><a href="/news/1">news</a><a href="/">home</a>`))
	mux.HandleFunc("/gallery/1", html(`<a href="/gallery/1/2">2</a>`))
	mux.HandleFunc("/gallery/1/2", html(`<img src="/target.png">`))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		outside.Add(1)
		html(`<img src="/decoy.png">`)(w, r)
	})
	mux.HandleFunc("/target.png", func(w http.ResponseWriter, r *http.Request) { w.Write(target) })
	mux.HandleFunc("/decoy.png", func(w http.ResponseWriter, r *http.Request) { w.Write(decoy) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hash := imageHash(t, writeFile(t, "target.png", string(target)))
	code, out, errOut := runCLI(t, srv.URL+"/gallery/", hash, "--stay-under", "--max-pages", "0")
	if code != exitOK || !strings.Contains(out, "=== 1 images hashed, 0 failed") || !strings.Contains(out, "page:  "+srv.URL+"/gallery/1/2\n") {
		t.Errorf("exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	if n := outside.Load(); n != 0 {
		t.Errorf("pages outside /gallery/ fetched %d times", n)
	}
}

func TestNoMatch(t *testing.T) {
	srv, file := testSite(t)
	hash := imageHash(t, file)
	code, out, _ := runCLI(t, srv.URL, hash, "--visit-images-regex", "^"+regexp.QuoteMeta(srv.URL)+"/(nothing|other)")
	if code != exitOK || !strings.Contains(out, "=== 1 images hashed, 0 failed") || !strings.Contains(out, "no match: best distance") {
		t.Errorf("exit %d, output %q", code, out)
	}
}

func TestPageLimitNote(t *testing.T) {
	srv, file := testSite(t)
	hash := imageHash(t, file)
	code, out, errOut := runCLI(t, srv.URL, hash, "--max-pages", "1", "--visit-images-regex", "/other")
	if code != exitOK || !strings.Contains(out, "no match: best distance") || !strings.Contains(out, "note: the crawl stopped at --max-pages 1 with 1 pages unvisited") || !strings.Contains(errOut, "stopped at --max-pages 1: 1 pages left unvisited") {
		t.Errorf("exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	code, out, _ = runCLI(t, srv.URL, hash, "--visit-images-regex", "/other")
	if code != exitOK || strings.Contains(out, "--max-pages") {
		t.Errorf("note shown although the whole site was crawled:\n%s", out)
	}
}

func TestRuntimeErrors(t *testing.T) {
	srv, file := testSite(t)
	hash := imageHash(t, file)
	cases := map[string]struct {
		args      []string
		stdout    string
		stderr    string
		notStdout string
	}{
		"nothing could be hashed": {
			[]string{srv.URL + "/broken", hash, "--max-pages", "1"},
			"=== 0 images hashed, 1 failed",
			"picfind: none of 1 images could be hashed (-v shows why)",
			"no match",
		},
		"images regex filters out everything": {
			[]string{srv.URL, hash, "--visit-images-regex", `^https://img\.example\.com/`},
			"",
			"picfind: none of the 2 image URLs found match --visit-images-regex",
			"===",
		},
	}
	for name, tc := range cases {
		code, out, errOut := runCLI(t, tc.args...)
		if code != exitRuntime || !strings.Contains(out, tc.stdout) || !strings.Contains(errOut, tc.stderr) || strings.Contains(out, tc.notStdout) || strings.Contains(errOut, "no images found") {
			t.Errorf("%s: exit %d\nstdout %s\nstderr %s", name, code, out, errOut)
		}
	}
}

func TestTemplateAndURLsFile(t *testing.T) {
	srv, file := testSite(t)
	hash := imageHash(t, file)
	want := "match: distance 0\n  image: " + srv.URL + "/target.png\n  page:  " + srv.URL + "/thread-1\n"
	code, out, errOut := runCLI(t, srv.URL+"/forum-1", hash, "--page-template", srv.URL+"/forum-{n}", "--pages", "2-3", "--visit-pages-regex", `/thread-\d+`)
	if code != exitOK || !strings.Contains(out, want) {
		t.Errorf("template: exit %d\n%s\n%s", code, out, errOut)
	}
	list := writeFile(t, "pages.txt", "# listing pages\n"+srv.URL+"/forum-1  # first\n\n"+srv.URL+"/forum-1\n")
	code, out, errOut = runCLI(t, hash, "--urls-file", list, "--visit-pages-regex", `/thread-\d+`)
	if code != exitOK || !strings.Contains(out, want) {
		t.Errorf("urls-file: exit %d\n%s\n%s", code, out, errOut)
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if code, _, errOut := runCLI(t, url, "3c7e7e3c18183c7e"); code != exitUnreachable {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

func TestInterruptedBeforeCrawl(t *testing.T) {
	srv, _ := testSite(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if code := run(ctx, context.Background(), []string{srv.URL, "3c7e7e3c18183c7e"}, &out, &errOut); code != exitInterrupted || out.Len() != 0 || !strings.Contains(errOut.String(), "picfind: interrupted") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

type markWriter struct {
	bytes.Buffer
	mark string
	seen chan struct{}
}

func (w *markWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if bytes.Contains(p, []byte(w.mark)) {
		close(w.seen)
	}
	return n, err
}

func TestInterrupt(t *testing.T) {
	const hashingLog = "interrupted: finishing the "
	cases := []struct {
		name, stopCrawlAt, stopHashAt, stats string
		afterCrawl, hashingLog               bool
	}{
		{"Ctrl-C while crawling hashes the images found so far", "/p2", "", "=== 2 images hashed, 0 failed", false, true},
		{"second Ctrl-C stops hashing", "/p2", "/b.png", "=== 1 images hashed, 0 failed", false, true},
		{"Ctrl-C while hashing stops it", "/b.png", "", "=== 1 images hashed, 0 failed", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			crawlCtx, stopCrawl := context.WithCancel(context.Background())
			defer stopCrawl()
			hashCtx, stopHash := context.WithCancel(context.Background())
			defer stopHash()
			var out bytes.Buffer
			errOut := &markWriter{mark: "crawl done:", seen: make(chan struct{})}
			pages := map[string]string{"/": `<a href="/p2">2</a><img src="/a.png"><img src="/b.png">`, "/p2": `<p>2</p>`}
			images := map[string][]byte{"/a.png": testPNG(t, 7), "/b.png": testPNG(t, 2)}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for path, stop := range map[string]context.CancelFunc{tc.stopCrawlAt: stopCrawl, tc.stopHashAt: stopHash} {
					if r.URL.Path == path {
						if tc.afterCrawl {
							select {
							case <-errOut.seen:
							case <-time.After(5 * time.Second):
							}
						}
						stop()
						select {
						case <-r.Context().Done():
						case <-time.After(5 * time.Second):
						}
						return
					}
				}
				if img, ok := images[r.URL.Path]; ok {
					w.Write(img)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, pages[r.URL.Path])
			}))
			t.Cleanup(srv.Close)
			code := run(crawlCtx, hashCtx, []string{srv.URL, "3c7e7e3c18183c7e", "--workers", "1"}, &out, errOut)
			if code != exitInterrupted || !strings.Contains(out.String(), tc.stats) || !strings.Contains(errOut.String(), "picfind: interrupted") || strings.Contains(errOut.String(), hashingLog) != tc.hashingLog {
				t.Errorf("exit %d\nstdout %s\nstderr %s", code, out.String(), errOut.String())
			}
		})
	}
}
