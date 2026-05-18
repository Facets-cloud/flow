package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// githubLatestReleaseURL is the canonical endpoint for the most-recent
// public release of flow. The SessionStart hook calls this at most
// once per cacheTTL to surface upgrade availability to Claude.
const githubLatestReleaseURL = "https://api.github.com/repos/Facets-cloud/flow/releases/latest"

// versionCacheTTL is how long a cached lookup is considered fresh.
// 24h is a deliberate trade-off: long enough that we don't hit
// GitHub on every Claude session start (sessions can fire several
// times a day during active work), short enough that a published
// release is noticed within a working day.
const versionCacheTTL = 24 * time.Hour

// versionFetchTimeout caps the synchronous network call inside the
// SessionStart hook. The hook latency budget is forgiving but not
// unlimited — 2 seconds is enough for a healthy connection while
// preventing a stalled network from blocking session start.
const versionFetchTimeout = 2 * time.Second

// httpGetForVersion is the function used by LatestRelease to fetch
// the GitHub releases endpoint. Tests override this to avoid real
// network traffic.
var httpGetForVersion = func(url string) (*http.Response, error) {
	client := &http.Client{Timeout: versionFetchTimeout}
	return client.Get(url)
}

// nowForVersion is the time source for cache freshness checks. Tests
// override this to make the TTL boundary deterministic.
var nowForVersion = time.Now

// versionCache is the on-disk shape of the cache file at
// ~/.flow/.version-cache.json.
type versionCache struct {
	CheckedAt     time.Time `json:"checkedAt"`
	LatestVersion string    `json:"latestVersion"`
}

// LatestRelease returns the latest tagged version of flow on GitHub
// as a tag string (e.g. "v0.1.0-alpha.3"), or "" if the lookup
// cannot be completed (offline, rate-limited, $FLOW_ROOT unwritable,
// JSON malformed, …). All failure modes are silent — version-check
// is best-effort plumbing, never load-bearing.
func LatestRelease() string {
	cachePath := versionCachePath()
	if cachePath == "" {
		return ""
	}
	if v, ok := readCachedVersion(cachePath); ok {
		return v
	}
	return fetchAndCacheLatest(cachePath)
}

func versionCachePath() string {
	root, err := flowRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, ".version-cache.json")
}

func readCachedVersion(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var c versionCache
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", false
	}
	if nowForVersion().Sub(c.CheckedAt) > versionCacheTTL {
		return "", false
	}
	return c.LatestVersion, true
}

func fetchAndCacheLatest(cachePath string) string {
	resp, err := httpGetForVersion(githubLatestReleaseURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return ""
	}
	if rel.TagName == "" {
		return ""
	}
	cache := versionCache{
		CheckedAt:     nowForVersion(),
		LatestVersion: rel.TagName,
	}
	raw, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return rel.TagName
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		_ = os.WriteFile(cachePath, raw, 0o644)
	}
	return rel.TagName
}
