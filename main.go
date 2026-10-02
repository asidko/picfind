package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/asidko/picfind/internal/ahash"
	"github.com/asidko/picfind/internal/crawl"
)

var version = "dev"

const description = "find the web page that shows an image, by its aHash"

const (
	exitOK          = 0
	exitRuntime     = 1
	exitUsage       = 2
	exitUnreachable = 3
	exitInterrupted = 130
)

const usageText = `usage:
  picfind <url> <hash>                crawl from <url>, rank its images by distance to <hash>
  picfind <url> --image-file <file>   same, the hash is taken from a local image
  picfind --image-file <file>         print the aHash of a local image
  picfind <hash> --urls-file <file>   search only the pages listed in <file>

<hash> is a 16-hex aHash, or a 32-hex hash whose first 16 hex are the aHash.
Distance 0-4 means the same image (see --max-dist).`

const examplesText = `examples:
  picfind https://example.com 3c7e7e3c18183c7e
  picfind https://example.com --image-file photo.jpg --max-pages 2000
  picfind https://example.com/forum.html 3c7e7e3c18183c7e --page-template 'https://example.com/forum-{n}.html' --pages 2-100
  picfind https://example.com/gallery/ 3c7e7e3c18183c7e --stay-under
  picfind 3c7e7e3c18183c7e --urls-file pages.txt --visit-pages-regex '/thread-[0-9]+' --visit-images-regex '^https://img\.example\.com/'

exit codes: 0 done (match or not), 1 runtime error, 2 usage error, 3 site unreachable, 130 interrupted`

var optionGroups = []struct {
	title string
	names []string
}{
	{"what to look for", []string{"image-file"}},
	{"pages to visit", []string{"max-pages", "stay-under", "all-hosts", "visit-pages-regex", "page-template", "pages", "urls-file"}},
	{"images to hash", []string{"visit-images-regex"}},
	{"output and speed", []string{"max-dist", "top", "workers", "page-workers", "v", "version", "help"}},
}

const maxPageRange = 1_000_000

var hashArg = regexp.MustCompile(`^[0-9a-fA-F]{16}([0-9a-fA-F]{16})?$`)

type options struct {
	imageFile, pageTemplate, pages, urlsFile string
	pagesRegex, imagesRegex                  string
	maxPages, workers, pageWorkers, top      int
	maxDist                                  int
	allHosts, stayUnder, verbose, help       bool
	version                                  bool
	set                                      map[string]bool
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return usageError{fmt.Sprintf(format, args...)}
}

func main() {
	crawlCtx, stopCrawl := context.WithCancel(context.Background())
	hashCtx, stopHash := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		stopCrawl()
		<-sigs
		stopHash()
		signal.Stop(sigs)
	}()
	os.Exit(run(crawlCtx, hashCtx, os.Args[1:], os.Stdout, os.Stderr))
}

func banner() string {
	commit := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		dirty := false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) >= 7 {
					commit = s.Value[:7]
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if dirty && commit != "unknown" {
			commit += "-dirty"
		}
	}
	return fmt.Sprintf("picfind %s (%s) - %s", version, commit, description)
}

func newFlags(o *options) *flag.FlagSet {
	fs := flag.NewFlagSet("picfind", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.imageFile, "image-file", "", "take the hash from local image `file` instead of <hash>")
	fs.IntVar(&o.maxPages, "max-pages", 0, "crawl at most `n` pages (default 0 = no limit)")
	fs.BoolVar(&o.stayUnder, "stay-under", false, "only go deeper under the path of <url>, never wider")
	fs.BoolVar(&o.allHosts, "all-hosts", false, "crawl links to other hosts too")
	fs.StringVar(&o.pageTemplate, "page-template", "", "numbered listing pages: `url` with {n}, <url> is page 1")
	fs.StringVar(&o.pages, "pages", "", "page `range` for --page-template, e.g. 2-100")
	fs.StringVar(&o.urlsFile, "urls-file", "", "search only the pages in `file`, one URL per line")
	fs.StringVar(&o.pagesRegex, "visit-pages-regex", "", "only visit pages whose URL matches `regex` (the start page always);\nwith --page-template/--urls-file: also visit matching links one hop away;\nlinks ending in .jpg/.png/... are visited as pages only when they match")
	fs.StringVar(&o.imagesRegex, "visit-images-regex", "", "only hash images whose URL matches `regex`")
	fs.IntVar(&o.maxDist, "max-dist", 4, "largest distance `d` that counts as a match")
	fs.IntVar(&o.top, "top", 20, "show the `n` closest images")
	fs.IntVar(&o.workers, "workers", 16, "download `n` images in parallel")
	fs.IntVar(&o.pageWorkers, "page-workers", 16, "fetch `n` pages in parallel")
	fs.BoolVar(&o.verbose, "v", false, "log every page and image that failed")
	fs.BoolVar(&o.version, "version", false, "print version and exit")
	fs.BoolVar(&o.help, "help", false, "show this help")
	fs.BoolVar(&o.help, "h", false, "show this help")
	return fs
}

func help(fs *flag.FlagSet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n", banner(), usageText)
	for _, g := range optionGroups {
		fmt.Fprintf(&b, "\n%s:\n", g.title)
		for _, name := range g.names {
			f := fs.Lookup(name)
			meta, text := flag.UnquoteUsage(f)
			left := "--" + name
			switch name {
			case "v":
				left = "-v"
			case "help":
				left = "-h, --help"
			}
			if meta != "" {
				left += " <" + meta + ">"
			}
			if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
				text += " (default " + f.DefValue + ")"
			}
			fmt.Fprintf(&b, "  %-30s %s\n", left, strings.ReplaceAll(text, "\n", "\n"+strings.Repeat(" ", 33)))
		}
	}
	fmt.Fprintf(&b, "\n%s\n", examplesText)
	return b.String()
}

func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if name, ok := strings.CutPrefix(err.Error(), "flag provided but not defined: "); ok {
				return nil, usagef("unknown option: %s", name)
			}
			return nil, usageError{err.Error()}
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func run(crawlCtx, hashCtx context.Context, args []string, stdout, stderr io.Writer) int {
	var o options
	fs := newFlags(&o)
	positional, err := parse(fs, args)
	if err != nil {
		return fail(stderr, err)
	}
	if o.help || len(args) == 0 {
		fmt.Fprint(stdout, help(fs))
		return exitOK
	}
	if o.version {
		fmt.Fprintln(stdout, banner())
		return exitOK
	}
	o.set = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { o.set[f.Name] = true })
	err = search(crawlCtx, hashCtx, &o, positional, stdout, stderr)
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(stderr, "picfind: interrupted")
		return exitInterrupted
	default:
		return fail(stderr, err)
	}
}

type unreachableError struct{ msg string }

func (e unreachableError) Error() string { return e.msg }

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "picfind: %s\n", err)
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(stderr, "run 'picfind --help' for usage")
		return exitUsage
	}
	var unreachable unreachableError
	if errors.As(err, &unreachable) {
		return exitUnreachable
	}
	return exitRuntime
}

func splitPositional(positional []string) (start, hash string, err error) {
	for _, p := range positional {
		lower := strings.ToLower(p)
		isURL := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
		isHash := hashArg.MatchString(p)
		switch {
		case !isURL && !isHash:
			return "", "", usagef("%q is neither a 16/32-hex hash nor an http(s):// URL", p)
		case isURL && start == "":
			start = p
		case isHash && hash == "":
			hash = p
		default:
			return "", "", usagef("unexpected argument: %s", p)
		}
	}
	return start, hash, nil
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

func validate(o *options) error {
	switch {
	case o.workers < 1 || o.pageWorkers < 1:
		return usagef("--workers and --page-workers must be at least 1")
	case o.top < 1:
		return usagef("--top must be at least 1")
	case o.maxPages < 0:
		return usagef("--max-pages must be 0 (no limit) or more")
	case o.maxDist < 0 || o.maxDist > 64:
		return usagef("--max-dist must be 0-64")
	case o.urlsFile != "" && o.pageTemplate != "":
		return usagef("use either --urls-file or --page-template, not both")
	case (o.urlsFile != "" || o.pageTemplate != "") && (o.set["max-pages"] || o.set["all-hosts"] || o.set["stay-under"]):
		return usagef("--max-pages, --stay-under and --all-hosts only apply when crawling from <url>")
	case o.stayUnder && o.allHosts:
		return usagef("use either --stay-under or --all-hosts, not both")
	case (o.pageTemplate == "") != (o.pages == ""):
		return usagef("--page-template and --pages go together")
	}
	return nil
}

func target(o *options, hash string) (ahash.Hash, error) {
	switch {
	case o.imageFile != "" && hash != "":
		return 0, usagef("give either <hash> or --image-file, not both")
	case o.imageFile != "":
		f, err := os.Open(o.imageFile)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		h, err := ahash.FromReader(f)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", o.imageFile, err)
		}
		return h, nil
	case hash != "":
		h, err := ahash.Parse(hash)
		if err != nil {
			return 0, usagef("invalid hash %q: %s", hash, err)
		}
		return h, nil
	}
	return 0, usagef("missing <hash> or --image-file")
}

func search(crawlCtx, hashCtx context.Context, o *options, positional []string, stdout, stderr io.Writer) error {
	start, hash, err := splitPositional(positional)
	if err != nil {
		return err
	}
	if start != "" && !httpURL(start) {
		return usagef("not an http(s) URL: %s", start)
	}
	if err := validate(o); err != nil {
		return err
	}
	if start == "" && o.pageTemplate != "" {
		return usagef("--page-template needs <url>, the first listing page")
	}
	if start == "" && o.urlsFile == "" {
		if o.imageFile == "" {
			if hash != "" {
				return usagef("missing <url> or --urls-file to search")
			}
			return usagef("missing <url>")
		}
		h, err := target(o, hash)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, h)
		return nil
	}
	if start != "" && o.urlsFile != "" {
		return usagef("--urls-file replaces <url>, give only one")
	}
	pagesRe, err := compileFlag("visit-pages-regex", o.pagesRegex)
	if err != nil {
		return err
	}
	imagesRe, err := compileFlag("visit-images-regex", o.imagesRegex)
	if err != nil {
		return err
	}
	var pageURLs []string
	switch {
	case o.urlsFile != "":
		if pageURLs, err = readURLs(o.urlsFile); err != nil {
			return err
		}
	case o.pageTemplate != "":
		if pageURLs, err = expandPages(o.pageTemplate, o.pages); err != nil {
			return err
		}
		pageURLs = append([]string{start}, pageURLs...)
	}
	want, err := target(o, hash)
	if err != nil {
		return err
	}

	logf := log.New(stderr, "", log.Ltime).Printf
	client := crawl.NewClient(max(o.workers, o.pageWorkers))
	client.Logf = logf
	if o.verbose {
		client.Debugf = logf
	}
	logf("target aHash: %s", want)

	hashing, stopHashing := context.WithCancel(hashCtx)
	defer stopHashing()
	found := foundPrinter(stdout, o.maxDist)
	hasher := client.NewHasher(hashing, want, o.workers, found)
	kept := 0
	client.OnImage = func(image, page string) {
		if imagesRe == nil || imagesRe.MatchString(image) {
			kept++
			hasher.Add(image, page)
		}
	}
	var col *crawl.Collection
	if pageURLs == nil {
		col = client.Crawl(crawlCtx, start, crawl.CrawlOptions{MaxPages: o.maxPages, AllHosts: o.allHosts, StayUnder: o.stayUnder, Pages: pagesRe, Workers: o.pageWorkers})
	} else {
		col = client.List(crawlCtx, pageURLs, pagesRe, o.pageWorkers)
	}
	interrupted := crawlCtx.Err() != nil
	if interrupted {
		logf("interrupted: finishing the %d images already queued, Ctrl-C again to stop", hasher.Pending())
	} else {
		defer context.AfterFunc(crawlCtx, stopHashing)()
		logf("crawl done: %d pages, %d images found, %d still to hash", col.PagesTried, len(col.Images), hasher.Pending())
	}
	if imagesRe != nil {
		logf("--visit-images-regex kept %d of %d images", kept, len(col.Images))
	}
	results, failed := hasher.Wait()
	switch {
	case !interrupted && col.PagesOK == 0:
		return unreachableError{fmt.Sprintf("could not fetch any of %d pages (site down, blocked, or not HTML; -v shows why)", col.PagesTried)}
	case kept == 0 && interrupted:
		return context.Canceled
	case kept == 0 && len(col.Images) > 0:
		return fmt.Errorf("none of the %d image URLs found match --visit-images-regex", len(col.Images))
	case kept == 0:
		return fmt.Errorf("no images found on %d pages", col.PagesOK)
	}
	if col.PagesLeft > 0 {
		logf("stopped at --max-pages %d: %d pages left unvisited", o.maxPages, col.PagesLeft)
	}
	report(stdout, results, failed, want, col.PagesLeft, o)
	switch {
	case crawlCtx.Err() != nil || hashing.Err() != nil:
		return context.Canceled
	case len(results) == 0:
		return fmt.Errorf("none of %d images could be hashed (-v shows why)", failed)
	}
	return nil
}

func foundPrinter(w io.Writer, maxDist int) func(crawl.Result) {
	tag := "[FOUND]"
	if f, ok := w.(*os.File); ok && os.Getenv("NO_COLOR") == "" {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			tag = "\x1b[1;32m[FOUND]\x1b[0m"
		}
	}
	var mu sync.Mutex
	return func(r crawl.Result) {
		if r.Distance > maxDist {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "%s distance %d  image: %s  page: %s\n", tag, r.Distance, r.Image, r.Page)
	}
}

func report(w io.Writer, results []crawl.Result, failed int, want ahash.Hash, pagesLeft int, o *options) {
	if len(results) == 0 && failed == 0 {
		return
	}
	fmt.Fprintf(w, "\n=== %d images hashed, %d failed | closest to %s ===\n", len(results), failed, want)
	if len(results) == 0 {
		return
	}
	fmt.Fprintf(w, "%4s  %-16s  %s\n", "dist", "hash", "image, then the page it is on")
	for _, r := range results[:min(o.top, len(results))] {
		fmt.Fprintf(w, "%4d  %s  %s\n%24s%s\n", r.Distance, r.Hash, r.Image, "", r.Page)
	}
	switch {
	case results[0].Distance <= o.maxDist:
		fmt.Fprintf(w, "match: distance %d\n  image: %s\n  page:  %s\n", results[0].Distance, results[0].Image, results[0].Page)
		more := 0
		for _, r := range results[1:] {
			if r.Distance <= o.maxDist {
				more++
			}
		}
		if more > 0 {
			fmt.Fprintf(w, "note: %d more within distance %d - resized copies or look-alikes, check the list\n", more, o.maxDist)
		}
	default:
		fmt.Fprintf(w, "no match: best distance %d (a match is 0-%d)\n", results[0].Distance, o.maxDist)
		if pagesLeft > 0 {
			fmt.Fprintf(w, "note: the crawl stopped at --max-pages %d with %d pages unvisited - rerun without --max-pages to search them all\n", o.maxPages, pagesLeft)
		}
	}
}

func compileFlag(name, expr string) (*regexp.Regexp, error) {
	if expr == "" {
		return nil, nil
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, usagef("invalid --%s: %s", name, err)
	}
	return re, nil
}

func expandPages(template, spec string) ([]string, error) {
	if !strings.Contains(template, "{n}") {
		return nil, usagef("--page-template must contain {n}")
	}
	if first := strings.ReplaceAll(template, "{n}", "1"); !httpURL(first) {
		return nil, usagef("--page-template is not an http(s) URL: %s", first)
	}
	a, b, ok := strings.Cut(spec, "-")
	from, err1 := strconv.Atoi(strings.TrimSpace(a))
	to, err2 := strconv.Atoi(strings.TrimSpace(b))
	if !ok || err1 != nil || err2 != nil || from < 0 || from > to {
		return nil, usagef("--pages must be START-END with START <= END, e.g. 2-100")
	}
	if to-from >= maxPageRange {
		return nil, usagef("--pages may cover at most %d pages", maxPageRange)
	}
	urls := make([]string, 0, to-from+1)
	for n := from; n <= to; n++ {
		urls = append(urls, strings.ReplaceAll(template, "{n}", strconv.Itoa(n)))
	}
	return urls, nil
}

func readURLs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	var urls []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || seen[line] {
			continue
		}
		if !httpURL(line) {
			return nil, usagef("%s:%d: not an http(s) URL: %s", path, n, line)
		}
		seen[line] = true
		urls = append(urls, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(urls) == 0 {
		return nil, usagef("%s lists no URLs", path)
	}
	return urls, nil
}
