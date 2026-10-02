package crawl

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/asidko/picfind/internal/ahash"
)

type Result struct {
	Distance int
	Hash     ahash.Hash
	Page     string
	Image    string
}

type job struct{ image, page string }

type Hasher struct {
	c        *Client
	ctx      context.Context
	target   ahash.Hash
	onResult func(Result)
	jobs     chan job
	wg       sync.WaitGroup
	mu       sync.Mutex
	results  []Result
	failed   int
	offered  atomic.Int64
	queued   atomic.Int64
	done     atomic.Int64
	full     atomic.Bool
}

func (c *Client) NewHasher(ctx context.Context, target ahash.Hash, workers int, onResult func(Result)) *Hasher {
	h := &Hasher{c: c, ctx: ctx, target: target, onResult: onResult, jobs: make(chan job, workers*64)}
	for range workers {
		h.wg.Go(h.work)
	}
	return h
}

func (h *Hasher) Add(image, page string) {
	h.offered.Add(1)
	select {
	case h.jobs <- job{image, page}:
		h.queued.Add(1)
		return
	default:
	}
	if !h.full.Swap(true) {
		h.c.logf("hash queue full: crawling pauses until hashing catches up")
	}
	select {
	case h.jobs <- job{image, page}:
		h.queued.Add(1)
	case <-h.ctx.Done():
	}
}

func (h *Hasher) Pending() int {
	return int(h.queued.Load() - h.done.Load())
}

func (h *Hasher) work() {
	for j := range h.jobs {
		if h.ctx.Err() != nil {
			continue
		}
		r, err := h.hash(j)
		if h.ctx.Err() != nil {
			continue
		}
		h.mu.Lock()
		if err != nil {
			h.failed++
		} else {
			h.results = append(h.results, r)
		}
		h.mu.Unlock()
		if err != nil {
			h.c.debugf("image %s: %v", j.image, err)
		} else if h.onResult != nil {
			h.onResult(r)
		}
		if n := h.done.Add(1); n%500 == 0 {
			h.c.logf("images hashed: %d of %d found so far", n, h.offered.Load())
		}
	}
}

func (h *Hasher) hash(j job) (Result, error) {
	res, err := h.c.get(h.ctx, j.image, imageTimeout, imageLimit, false)
	if err != nil {
		return Result{}, err
	}
	v, err := ahash.FromReader(bytes.NewReader(res.body))
	if err != nil {
		return Result{}, err
	}
	return Result{Distance: ahash.Distance(v, h.target), Hash: v, Page: j.page, Image: j.image}, nil
}

func (h *Hasher) Wait() (results []Result, failed int) {
	close(h.jobs)
	h.wg.Wait()
	slices.SortStableFunc(h.results, func(a, b Result) int {
		if a.Distance != b.Distance {
			return a.Distance - b.Distance
		}
		return strings.Compare(a.Image, b.Image)
	})
	h.c.logf("images hashed: %d", h.done.Load())
	return h.results, h.failed
}
