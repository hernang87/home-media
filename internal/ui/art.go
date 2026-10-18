package ui

import (
	"container/list"
	"context"
	"sync"
	"time"

	"fyne.io/fyne/v2"
)

// ArtCache fetches artwork in the background and keeps the most recent images.
type ArtCache struct {
	fetch func(ctx context.Context, rel *string) ([]byte, error)
	max   int

	mu       sync.Mutex
	ll       *list.List // front = most recently used; values are *artEntry
	items    map[string]*list.Element
	inflight map[string][]func(fyne.Resource)
}

type artEntry struct {
	key string
	res fyne.Resource
}

// NewArtCache creates a cache holding up to max images.
func NewArtCache(max int, fetch func(ctx context.Context, rel *string) ([]byte, error)) *ArtCache {
	return &ArtCache{fetch: fetch, max: max, ll: list.New(), items: map[string]*list.Element{}, inflight: map[string][]func(fyne.Resource){}}
}

// Get calls cb with the image for rel on the UI thread (immediately when cached).
// cb is not called when rel is nil or the download fails.
func (c *ArtCache) Get(rel *string, cb func(fyne.Resource)) {
	if rel == nil || *rel == "" {
		return
	}
	key := *rel
	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		res := el.Value.(*artEntry).res
		c.mu.Unlock()
		cb(res)
		return
	}
	waiting := c.inflight[key]
	c.inflight[key] = append(waiting, cb)
	c.mu.Unlock()
	if len(waiting) > 0 {
		return // a download for this image is already running
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		data, err := c.fetch(ctx, rel)
		c.mu.Lock()
		cbs := c.inflight[key]
		delete(c.inflight, key)
		var res fyne.Resource
		if err == nil {
			res = fyne.NewStaticResource(key, data)
			c.items[key] = c.ll.PushFront(&artEntry{key, res})
			for c.ll.Len() > c.max {
				last := c.ll.Back()
				c.ll.Remove(last)
				delete(c.items, last.Value.(*artEntry).key)
			}
		}
		c.mu.Unlock()
		if res == nil {
			return
		}
		fyne.Do(func() {
			for _, f := range cbs {
				f(res)
			}
		})
	}()
}
