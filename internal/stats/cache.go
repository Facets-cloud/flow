package stats

import (
	"encoding/json"
	"os"
)

type cacheEntry struct {
	ModNS  int64      `json:"mod_ns"`
	Size   int64      `json:"size"`
	Rollup FileRollup `json:"rollup"`
}

// Cache maps a jsonl path to its last-scanned rollup, keyed by file
// identity (mod time + size) so unchanged files are not rescanned.
type Cache struct {
	Entries map[string]cacheEntry `json:"entries"`
}

// LoadCache reads a cache file. A missing or corrupt file yields an empty
// (usable) cache — never an error. Stats must never fail on a bad cache.
func LoadCache(path string) *Cache {
	c := &Cache{Entries: map[string]cacheEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var loaded Cache
	if err := json.Unmarshal(data, &loaded); err != nil || loaded.Entries == nil {
		return c
	}
	return &loaded
}

// Save writes the cache as JSON.
func (c *Cache) Save(path string) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ScanFile returns the rollup for a jsonl file, reusing the cached result
// when the file's mod time and size are unchanged.
func (c *Cache) ScanFile(path, ownSlug string) (FileRollup, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return FileRollup{}, err
	}
	modNS, size := fi.ModTime().UnixNano(), fi.Size()
	if e, ok := c.Entries[path]; ok && e.ModNS == modNS && e.Size == size {
		return e.Rollup, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return FileRollup{}, err
	}
	defer f.Close()
	roll, err := ScanJSONL(f, ownSlug)
	if err != nil {
		return FileRollup{}, err
	}
	c.Entries[path] = cacheEntry{ModNS: modNS, Size: size, Rollup: roll}
	return roll, nil
}

// Prune drops cache entries whose path was not seen in the latest run.
func (c *Cache) Prune(seen map[string]bool) {
	for p := range c.Entries {
		if !seen[p] {
			delete(c.Entries, p)
		}
	}
}
