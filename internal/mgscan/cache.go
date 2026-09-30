package mgscan

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Row is the subset of one `mg list --json` (NDJSON) line a whole-store scan
// needs: the id to show, the CURRENT status, and the file mtime that keys the
// cache.
type Row struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Mtime  string `json:"mtime"`
}

// ParseList parses `mg list --json` output. Blank lines are skipped; a line that
// is not JSON is an error, because a scan that silently drops unparseable rows
// under-counts the store it is reporting on.
func ParseList(raw []byte) ([]Row, error) {
	var out []Row
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("parsing mg list output: %w", err)
		}
		out = append(out, r)
	}
	return out, nil
}

// Cache holds one derived value per work item, valid for as long as the item's
// file mtime — as `mg list --json` reports it — is unchanged. A scan consults it
// before forking `mg show`, so a pass over an unchanged store costs its `mg list`
// calls and nothing else (drellem2/pogo#179: 88% of the live store is archived
// and never changes, yet every pass re-read every body).
//
// Why the mtime is a sound key: every edit to an item's body rewrites its file,
// which moves the mtime. The one store operation that does NOT move it is a
// status change — mg moves the file between status directories with a rename,
// and a rename keeps the mtime (internal/workitem/workitem.go). So a cached value
// must never carry an item's status; callers take status from the current list
// row, not from whatever `mg show` said when the value was cached.
//
// What a Cache does NOT key on, and so what callers must keep out of it:
//
//   - An empty mtime. A row without one cannot be validated, so Lookup always
//     misses and Store refuses it.
//   - An id listed more than once in a pass. A short id shared by two archived
//     twins names two files with two mtimes, and a single slot cannot hold both.
//     Callers bypass the cache for those ids (they are a dozen on the live store,
//     and the case most likely to regress quietly if a cache entry is ever
//     shared between twins).
//
// A Cache must be shared by POINTER. A scan source that is copied by value — the
// way pogod binds `src.Carriers` off a struct value — keeps working with a
// *Cache field and silently never hits with an embedded one. A nil *Cache is
// valid and caches nothing, which is what a one-shot CLI scan wants.
type Cache[V any] struct {
	mu sync.Mutex
	m  map[string]cacheEntry[V]
}

type cacheEntry[V any] struct {
	mtime string
	v     V
}

// NewCache returns an empty cache.
func NewCache[V any]() *Cache[V] {
	return &Cache[V]{m: map[string]cacheEntry[V]{}}
}

// Lookup returns the value cached for id, if it was cached at this exact mtime.
func (c *Cache[V]) Lookup(id, mtime string) (V, bool) {
	var zero V
	if c == nil || mtime == "" {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || e.mtime != mtime {
		return zero, false
	}
	return e.v, true
}

// Store caches v for id at mtime, replacing any older entry. An empty mtime is
// ignored: see the type comment.
func (c *Cache[V]) Store(id, mtime string, v V) {
	if c == nil || mtime == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cacheEntry[V]{}
	}
	c.m[id] = cacheEntry[V]{mtime: mtime, v: v}
}

// Forget drops id's entry, if any.
func (c *Cache[V]) Forget(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
}

// Retain drops every entry whose id is not in keep. A scan calls it with the ids
// it just listed, so an item that left the store (or became a twin) does not pin
// memory for the life of the daemon.
func (c *Cache[V]) Retain(keep map[string]bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.m {
		if !keep[id] {
			delete(c.m, id)
		}
	}
}

// Len reports how many entries the cache holds.
func (c *Cache[V]) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// Entry is one cached value with the mtime it is valid at — the on-disk form of
// a Cache (mg-257a8).
type Entry[V any] struct {
	Mtime string `json:"mtime"`
	Value V      `json:"value"`
}

// Snapshot returns a copy of every entry, for a caller that must carry the cache
// across processes. `pogo gh-watch` is such a caller: the scans that hold these
// caches now run in a process launchd starts every few minutes, and a cache
// that died with each run would return every pass to one `mg show` fork per
// item — the drellem2/pogo#179 cost the cache exists to remove.
//
// Persisting it changes nothing about correctness: an entry is still used only
// at the exact mtime it was stored under, so an item edited between runs misses.
func (c *Cache[V]) Snapshot() map[string]Entry[V] {
	out := map[string]Entry[V]{}
	if c == nil {
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, e := range c.m {
		out[id] = Entry[V]{Mtime: e.mtime, Value: e.v}
	}
	return out
}

// LoadEntries adds entries (typically a previous run's Snapshot) to the cache.
// Entries with no mtime are dropped, as Store drops them.
func (c *Cache[V]) LoadEntries(entries map[string]Entry[V]) {
	for id, e := range entries {
		c.Store(id, e.Mtime, e.Value)
	}
}

// MarshalJSON encodes the cache as its Snapshot, so a caller can persist a cache
// whose value type it cannot name (carrierdrift's is unexported).
func (c *Cache[V]) MarshalJSON() ([]byte, error) { return json.Marshal(c.Snapshot()) }

// UnmarshalJSON adds the encoded entries to the cache; see LoadEntries.
func (c *Cache[V]) UnmarshalJSON(b []byte) error {
	var entries map[string]Entry[V]
	if err := json.Unmarshal(b, &entries); err != nil {
		return err
	}
	c.LoadEntries(entries)
	return nil
}
