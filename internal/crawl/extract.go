package crawl

import (
	"io"
	"net/url"
	"path"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

var (
	skipExts       = []string{".pdf", ".zip", ".tar", ".gz", ".mp4", ".mp3", ".avi", ".mov", ".rar", ".7z"}
	unhashableExts = []string{".svg", ".ico"}
	imageExts      = []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"}
	pageExts       = []string{".html", ".htm", ".shtml", ".xhtml", ".php", ".asp", ".aspx", ".jsp", ".cgi"}
)

func withoutFragment(u *url.URL) *url.URL {
	c := *u
	c.Fragment, c.RawFragment = "", ""
	return &c
}

func pageKey(u *url.URL) string {
	c := withoutFragment(u)
	c.Path = strings.TrimRight(c.Path, "/")
	c.RawPath = strings.TrimRight(c.RawPath, "/")
	return c.String()
}

func ext(u *url.URL) string {
	return strings.ToLower(path.Ext(u.Path))
}

func resolve(base *url.URL, raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return nil, false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	u := base.ResolveReference(ref)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false
	}
	return u, true
}

func followable(u *url.URL) bool {
	return !slices.Contains(skipExts, ext(u))
}

func isImage(u *url.URL) bool {
	return slices.Contains(imageExts, ext(u))
}

func extract(body io.Reader, base *url.URL) (links, images []string) {
	seenLinks, seenImages := map[string]bool{}, map[string]bool{}
	addLink := func(raw string) {
		if u, ok := resolve(base, raw); ok {
			if s := withoutFragment(u).String(); !seenLinks[s] {
				seenLinks[s] = true
				links = append(links, s)
			}
		}
	}
	addImage := func(raw string) {
		if u, ok := resolve(base, raw); ok && !slices.Contains(unhashableExts, ext(u)) {
			if s := u.String(); !seenImages[s] {
				seenImages[s] = true
				images = append(images, s)
			}
		}
	}
	addSrcset := func(srcset string) {
		const space = " \t\n\f\r"
		for s := srcset; ; {
			s = strings.TrimLeft(s, space+",")
			end := strings.IndexAny(s, space)
			if end < 0 {
				end = len(s)
			}
			if end == 0 {
				return
			}
			raw := s[:end]
			addImage(strings.TrimRight(raw, ","))
			s = s[end:]
			if !strings.HasSuffix(raw, ",") {
				_, s, _ = strings.Cut(s, ",")
			}
		}
	}
	z := html.NewTokenizer(body)
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return links, images
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, more := z.TagName()
		attrs := map[string]string{}
		for more {
			var k, v []byte
			k, v, more = z.TagAttr()
			attrs[string(k)] = string(v)
		}
		switch string(name) {
		case "base":
			if u, ok := resolve(base, attrs["href"]); ok {
				base = u
			}
		case "a":
			if u, ok := resolve(base, attrs["href"]); ok && isImage(u) {
				addImage(attrs["href"])
			}
			addLink(attrs["href"])
		case "noscript":
			z.NextIsNotRawText()
		case "img":
			for _, a := range []string{"src", "data-src", "data-original", "data-lazy-src"} {
				addImage(attrs[a])
			}
			addSrcset(attrs["srcset"])
		case "source":
			addSrcset(attrs["srcset"])
		case "meta":
			p := attrs["property"]
			if p == "og:image" || p == "twitter:image" || attrs["name"] == "twitter:image" {
				addImage(attrs["content"])
			}
		}
	}
}
