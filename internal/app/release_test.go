package app

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHTTPResponse builds a *http.Response with the given JSON body.
func fakeHTTPResponse(t *testing.T, status int, body string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
	}
}

// withFakeHTTP swaps httpGetForVersion for the duration of the test.
func withFakeHTTP(t *testing.T, fn func(url string) (*http.Response, error)) {
	t.Helper()
	old := httpGetForVersion
	httpGetForVersion = fn
	t.Cleanup(func() { httpGetForVersion = old })
}

// withFlowRoot points $FLOW_ROOT at a tempdir so version-cache writes
// don't touch the developer's real ~/.flow.
func withFlowRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FLOW_ROOT", dir)
	return dir
}

// TestLatestReleaseFetchesAndCaches verifies the happy path: no cache
// → HTTP fetch succeeds → tag returned and cache written.
func TestLatestReleaseFetchesAndCaches(t *testing.T) {
	root := withFlowRoot(t)
	withFakeHTTP(t, func(url string) (*http.Response, error) {
		return fakeHTTPResponse(t, 200, `{"tag_name":"v9.9.9"}`), nil
	})

	if got := LatestRelease(); got != "v9.9.9" {
		t.Errorf("LatestRelease = %q, want v9.9.9", got)
	}
	cachePath := filepath.Join(root, ".version-cache.json")
	if raw, err := os.ReadFile(cachePath); err != nil {
		t.Errorf("cache file not written: %v", err)
	} else if !strings.Contains(string(raw), "v9.9.9") {
		t.Errorf("cache file does not contain version: %s", raw)
	}
}

// TestLatestReleaseHonorsCache verifies a fresh cache short-circuits
// the HTTP path: even if the network is rigged to fail, a fresh
// cache returns the cached value.
func TestLatestReleaseHonorsCache(t *testing.T) {
	root := withFlowRoot(t)
	cachePath := filepath.Join(root, ".version-cache.json")
	if err := os.WriteFile(cachePath,
		[]byte(`{"checkedAt":"`+time.Now().Format(time.RFC3339)+`","latestVersion":"v0.0.1-from-cache"}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	withFakeHTTP(t, func(url string) (*http.Response, error) {
		t.Errorf("httpGetForVersion called when cache should have been used")
		return nil, http.ErrServerClosed
	})

	if got := LatestRelease(); got != "v0.0.1-from-cache" {
		t.Errorf("LatestRelease = %q, want v0.0.1-from-cache (cache hit)", got)
	}
}

// TestLatestReleaseCacheTTLBoundary verifies the cache is treated as
// stale once it exceeds versionCacheTTL.
func TestLatestReleaseCacheTTLBoundary(t *testing.T) {
	root := withFlowRoot(t)
	cachePath := filepath.Join(root, ".version-cache.json")
	stale := time.Now().Add(-versionCacheTTL - time.Hour).Format(time.RFC3339)
	if err := os.WriteFile(cachePath,
		[]byte(`{"checkedAt":"`+stale+`","latestVersion":"v-old"}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	hits := 0
	withFakeHTTP(t, func(url string) (*http.Response, error) {
		hits++
		return fakeHTTPResponse(t, 200, `{"tag_name":"v-fresh"}`), nil
	})

	if got := LatestRelease(); got != "v-fresh" {
		t.Errorf("LatestRelease with stale cache = %q, want v-fresh", got)
	}
	if hits != 1 {
		t.Errorf("expected 1 HTTP fetch on stale cache, got %d", hits)
	}
}

// TestLatestReleaseSilentOnNetworkFailure pins the silent-failure
// contract: every error path returns "" so the SessionStart hook
// never blocks or surfaces a stack trace.
func TestLatestReleaseSilentOnNetworkFailure(t *testing.T) {
	withFlowRoot(t)
	withFakeHTTP(t, func(url string) (*http.Response, error) {
		return nil, http.ErrServerClosed
	})

	if got := LatestRelease(); got != "" {
		t.Errorf("LatestRelease on network error = %q, want empty", got)
	}
}

// TestLatestReleaseSilentOnNon200 covers rate-limited responses.
func TestLatestReleaseSilentOnNon200(t *testing.T) {
	withFlowRoot(t)
	withFakeHTTP(t, func(url string) (*http.Response, error) {
		return fakeHTTPResponse(t, 429, `{"message":"API rate limit exceeded"}`), nil
	})

	if got := LatestRelease(); got != "" {
		t.Errorf("LatestRelease on 429 = %q, want empty", got)
	}
}

// TestAppendStaleVersionHintDevBuildIsEmpty pins that dev builds
// (Version=="dev" or "") never trigger the version check at all —
// running flow from `make build` shouldn't emit upgrade nudges.
func TestAppendStaleVersionHintDevBuildIsEmpty(t *testing.T) {
	old := Version
	Version = "dev"
	t.Cleanup(func() { Version = old })

	withFakeHTTP(t, func(url string) (*http.Response, error) {
		t.Error("httpGetForVersion should not be called for dev builds")
		return nil, nil
	})

	if got := appendStaleVersionHint(); got != "" {
		t.Errorf("dev build appendStaleVersionHint = %q, want empty", got)
	}
}

// TestAppendStaleVersionHintMatchingIsEmpty pins that when the local
// version matches the latest release, no nudge is appended.
func TestAppendStaleVersionHintMatchingIsEmpty(t *testing.T) {
	withFlowRoot(t)
	old := Version
	Version = "v0.1.0-alpha.3"
	t.Cleanup(func() { Version = old })

	withFakeHTTP(t, func(url string) (*http.Response, error) {
		return fakeHTTPResponse(t, 200, `{"tag_name":"v0.1.0-alpha.3"}`), nil
	})

	if got := appendStaleVersionHint(); got != "" {
		t.Errorf("matching version appendStaleVersionHint = %q, want empty", got)
	}
}

// TestAppendStaleVersionHintStaleEmitsHint pins the staleness signal
// shape: the hook must include "flow-version-stale:" so the §4.15
// trigger in the skill picks it up.
func TestAppendStaleVersionHintStaleEmitsHint(t *testing.T) {
	withFlowRoot(t)
	old := Version
	Version = "v0.1.0-alpha.3"
	t.Cleanup(func() { Version = old })

	withFakeHTTP(t, func(url string) (*http.Response, error) {
		return fakeHTTPResponse(t, 200, `{"tag_name":"v0.2.0"}`), nil
	})

	got := appendStaleVersionHint()
	for _, want := range []string{
		"flow-version-stale: v0.2.0",
		"v0.1.0-alpha.3",
		"§4.15",
		"Do not interrupt active work",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stale hint missing %q; got:\n%s", want, got)
		}
	}
}
