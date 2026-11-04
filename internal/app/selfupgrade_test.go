package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testReleaseURL = "https://example.test/flow-new"

// fakeBinary is a temporary file that stands in for the flow binary.
func fakeBinary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "flow")
	if err := os.WriteFile(p, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// withSelfUpgradeGates makes every gate of selfUpgradeOffReason pass,
// with execPath as the running binary. It returns the started upgrades.
func withSelfUpgradeGates(t *testing.T, execPath string) *[]string {
	t.Helper()
	withVersion(t, "v0.1.0-alpha.3")
	t.Setenv(selfUpgradeEnv, "")
	t.Setenv("CI", "")
	oldExec, oldStart, oldRoot, oldGOOS := selfUpgradeExec, startSelfUpgrade, selfUpgradeAsRoot, selfUpgradeGOOS
	t.Cleanup(func() {
		selfUpgradeExec, startSelfUpgrade, selfUpgradeAsRoot, selfUpgradeGOOS = oldExec, oldStart, oldRoot, oldGOOS
	})
	started := &[]string{}
	selfUpgradeExec = func() string { return execPath }
	startSelfUpgrade = func(p, root string) error {
		*started = append(*started, p)
		return nil
	}
	selfUpgradeAsRoot = func() bool { return false }
	selfUpgradeGOOS = "darwin"
	return started
}

// writeVersionCache writes a fresh version cache that names latest.
func writeVersionCache(t *testing.T, root, latest string) {
	t.Helper()
	raw, _ := json.Marshal(versionCache{CheckedAt: time.Now(), LatestVersion: latest})
	if err := os.WriteFile(filepath.Join(root, ".version-cache.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// withFakeRelease serves a release with tag and body as this
// platform's build, under the SHA-256 digest (computed from body when
// digest is "").
func withFakeRelease(t *testing.T, tag, body, digest string) {
	t.Helper()
	if digest == "" {
		sum := sha256.Sum256([]byte(body))
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	rel, _ := json.Marshal(map[string]any{
		"tag_name": tag,
		"assets": []map[string]string{{
			"name":                 "flow-" + runtime.GOOS + "-" + runtime.GOARCH,
			"digest":               digest,
			"browser_download_url": testReleaseURL,
		}},
	})
	old := httpGetForUpgrade
	t.Cleanup(func() { httpGetForUpgrade = old })
	httpGetForUpgrade = func(url string) (*http.Response, error) {
		switch url {
		case githubLatestReleaseURL:
			return fakeHTTPResponse(t, 200, string(rel)), nil
		case testReleaseURL:
			return fakeHTTPResponse(t, 200, body), nil
		}
		return nil, errors.New("unexpected URL " + url)
	}
}

func TestIsNewerRelease(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.1.0-alpha.29", "v0.1.0-alpha.3", true},
		{"v0.1.0-alpha.3", "v0.1.0-alpha.29", false},
		{"v0.1.0-alpha.29", "v0.1.0-alpha.29", false},
		{"v0.1.0", "v0.1.0-alpha.29", true},
		{"v0.1.0-alpha.29", "v0.1.0-alpha.24-1-g7ad6e4e", true},
		{"v0.1.0-alpha.29", "v0.1.0-alpha.29-3-gabc1234-dirty", false},
		{"v0.1.0-alpha.29", "dev", false},
		{"", "v0.1.0-alpha.3", false},
	}
	for _, c := range cases {
		if got := isNewerRelease(c.latest, c.current); got != c.want {
			t.Errorf("isNewerRelease(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestSelfUpgradeOffReason(t *testing.T) {
	bin := fakeBinary(t)
	cases := []struct {
		name  string
		setup func()
		exec  string
		want  string
	}{
		{"all gates pass", func() {}, bin, ""},
		{"dev build", func() { Version = "dev" }, bin, "dev"},
		{"source build", func() { Version = "v0.1.0-alpha.24-1-g7ad6e4e" }, bin, "dev"},
		{"kill switch", func() { t.Setenv(selfUpgradeEnv, "1") }, bin, "env"},
		{"CI", func() { t.Setenv("CI", "true") }, bin, "ci"},
		{"Linux", func() { selfUpgradeGOOS = "linux" }, bin, "platform"},
		{"root", func() { selfUpgradeAsRoot = func() bool { return true } }, bin, "root"},
		{"no binary", func() {}, "", "folder"},
		{"folder not writable", func() {}, filepath.Join(t.TempDir(), "missing", "flow"), "folder"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withSelfUpgradeGates(t, c.exec)
			c.setup()
			if got := selfUpgradeOffReason(c.exec); got != c.want {
				t.Errorf("selfUpgradeOffReason = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMaybeSelfUpgradeStartsForNewerRelease(t *testing.T) {
	root := withFlowRoot(t)
	bin := fakeBinary(t)
	started := withSelfUpgradeGates(t, bin)
	writeVersionCache(t, root, "v0.1.0-alpha.29")

	maybeSelfUpgrade()
	if len(*started) != 1 || (*started)[0] != bin {
		t.Fatalf("started = %v, want one upgrade of %s", *started, bin)
	}
	// The claim of the first upgrade stops a second one.
	maybeSelfUpgrade()
	if len(*started) != 1 {
		t.Errorf("started = %v, want the claim to stop a second upgrade", *started)
	}
}

func TestMaybeSelfUpgradeSkips(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{"no cache", func(t *testing.T, root string) {}},
		{"same version", func(t *testing.T, root string) { writeVersionCache(t, root, "v0.1.0-alpha.3") }},
		{"older release", func(t *testing.T, root string) { writeVersionCache(t, root, "v0.1.0-alpha.2") }},
		{"gate off", func(t *testing.T, root string) {
			writeVersionCache(t, root, "v0.1.0-alpha.29")
			t.Setenv(selfUpgradeEnv, "1")
		}},
		{"recent failure", func(t *testing.T, root string) {
			writeVersionCache(t, root, "v0.1.0-alpha.29")
			st := selfUpgradeState{Target: "v0.1.0-alpha.29", Result: "failed", At: time.Now().Add(-time.Hour)}
			if err := writeSelfUpgradeState(root, st); err != nil {
				t.Fatal(err)
			}
		}},
		{"live claim", func(t *testing.T, root string) {
			writeVersionCache(t, root, "v0.1.0-alpha.29")
			if err := os.WriteFile(filepath.Join(root, selfUpgradeClaimFile), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withFlowRoot(t)
			started := withSelfUpgradeGates(t, fakeBinary(t))
			c.setup(t, root)
			maybeSelfUpgrade()
			if len(*started) != 0 {
				t.Errorf("started = %v, want no upgrade", *started)
			}
		})
	}
}

func TestMaybeSelfUpgradeRetriesAfterOldFailure(t *testing.T) {
	root := withFlowRoot(t)
	started := withSelfUpgradeGates(t, fakeBinary(t))
	writeVersionCache(t, root, "v0.1.0-alpha.29")
	st := selfUpgradeState{Target: "v0.1.0-alpha.29", Result: "failed", At: time.Now().Add(-25 * time.Hour)}
	if err := writeSelfUpgradeState(root, st); err != nil {
		t.Fatal(err)
	}
	maybeSelfUpgrade()
	if len(*started) != 1 {
		t.Errorf("started = %v, want one upgrade after the retry pause", *started)
	}
}

func TestTakeClaimTakesOverDeadClaim(t *testing.T) {
	claim := filepath.Join(t.TempDir(), selfUpgradeClaimFile)
	now := time.Now()
	if !takeClaim(claim, now) {
		t.Fatal("first takeClaim = false, want true")
	}
	if takeClaim(claim, now) {
		t.Error("takeClaim on a live claim = true, want false")
	}
	if !takeClaim(claim, now.Add(2*selfUpgradeClaimTTL)) {
		t.Error("takeClaim on a dead claim = false, want true")
	}
}

func TestStartDetachedSelfUpgradeRefusesInTests(t *testing.T) {
	if err := startDetachedSelfUpgrade(fakeBinary(t), t.TempDir()); err == nil {
		t.Error("startDetachedSelfUpgrade in a test = nil error, want a refusal")
	}
}

func TestCmdSelfUpgradeReplacesBinary(t *testing.T) {
	root := withFlowRoot(t)
	bin := fakeBinary(t)
	withSelfUpgradeGates(t, bin)
	withFakeRelease(t, "v0.1.0-alpha.29", "new binary", "")
	if err := os.WriteFile(filepath.Join(root, selfUpgradeClaimFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if rc := cmdSelfUpgrade(); rc != 0 {
		t.Fatalf("cmdSelfUpgrade rc = %d, want 0", rc)
	}
	if got, _ := os.ReadFile(bin); string(got) != "new binary" {
		t.Errorf("binary = %q, want the new build", got)
	}
	if info, _ := os.Stat(bin); info.Mode().Perm() != 0o755 {
		t.Errorf("binary mode = %v, want 0755", info.Mode().Perm())
	}
	st, err := readSelfUpgradeState(root)
	if err != nil || st.Result != "upgraded" || st.From != "v0.1.0-alpha.3" || st.Target != "v0.1.0-alpha.29" {
		t.Errorf("state = %+v (err %v), want upgraded alpha.3 → alpha.29", st, err)
	}
	if _, err := os.Stat(filepath.Join(root, selfUpgradeClaimFile)); !os.IsNotExist(err) {
		t.Errorf("claim still exists after the upgrade (stat err %v)", err)
	}
	assertNoTempFiles(t, filepath.Dir(bin))
}

func TestCmdSelfUpgradeKeepsBinaryOnBadDigest(t *testing.T) {
	root := withFlowRoot(t)
	bin := fakeBinary(t)
	withSelfUpgradeGates(t, bin)
	withFakeRelease(t, "v0.1.0-alpha.29", "tampered binary", "sha256:"+strings.Repeat("0", 64))

	if rc := cmdSelfUpgrade(); rc != 1 {
		t.Fatalf("cmdSelfUpgrade rc = %d, want 1", rc)
	}
	if got, _ := os.ReadFile(bin); string(got) != "old binary" {
		t.Errorf("binary = %q, want the old binary kept", got)
	}
	st, _ := readSelfUpgradeState(root)
	if st.Result != "failed" || !strings.Contains(st.Error, "SHA-256") {
		t.Errorf("state = %+v, want a SHA-256 failure", st)
	}
	assertNoTempFiles(t, filepath.Dir(bin))
}

func TestCmdSelfUpgradeNothingToDo(t *testing.T) {
	withFlowRoot(t)
	bin := fakeBinary(t)
	withSelfUpgradeGates(t, bin)
	withFakeRelease(t, "v0.1.0-alpha.3", "same binary", "")

	if rc := cmdSelfUpgrade(); rc != 0 {
		t.Fatalf("cmdSelfUpgrade rc = %d, want 0", rc)
	}
	if got, _ := os.ReadFile(bin); string(got) != "old binary" {
		t.Errorf("binary = %q, want it unchanged", got)
	}
}

func TestAssetForNeedsDigest(t *testing.T) {
	name := "flow-" + runtime.GOOS + "-" + runtime.GOARCH
	rel := &githubRelease{TagName: "v1.0.0"}
	if _, _, err := assetFor(rel); err == nil || !strings.Contains(err.Error(), "no "+name) {
		t.Errorf("assetFor without assets: err = %v, want a missing-build error", err)
	}
	rel.Assets = append(rel.Assets, struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
		URL    string `json:"browser_download_url"`
	}{Name: name, URL: testReleaseURL})
	if _, _, err := assetFor(rel); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("assetFor without digest: err = %v, want a SHA-256 error", err)
	}
}

func TestCmdUpgrade(t *testing.T) {
	cases := []struct {
		name    string
		version string
		args    []string
		wantRC  int
		wantBin string
	}{
		{"up to date", "v0.1.0-alpha.29", nil, 0, "old binary"},
		{"check only", "v0.1.0-alpha.3", []string{"--check"}, 0, "old binary"},
		{"upgrade", "v0.1.0-alpha.3", nil, 0, "new binary"},
		{"source build refused", "v0.1.0-alpha.24-1-g7ad6e4e", nil, 1, "old binary"},
		{"source build forced", "v0.1.0-alpha.24-1-g7ad6e4e", []string{"--force"}, 0, "new binary"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withFlowRoot(t)
			bin := fakeBinary(t)
			withSelfUpgradeGates(t, bin)
			Version = c.version
			withFakeRelease(t, "v0.1.0-alpha.29", "new binary", "")
			if rc := cmdUpgrade(c.args); rc != c.wantRC {
				t.Errorf("cmdUpgrade rc = %d, want %d", rc, c.wantRC)
			}
			if got, _ := os.ReadFile(bin); string(got) != c.wantBin {
				t.Errorf("binary = %q, want %q", got, c.wantBin)
			}
		})
	}
}

func TestSelfUpgradeTrigger(t *testing.T) {
	for _, cmd := range []string{"__auto-exec", "__owner-tick", selfUpgradeArg, "upgrade", "--version", "help"} {
		if selfUpgradeTrigger(cmd) {
			t.Errorf("selfUpgradeTrigger(%q) = true, want false", cmd)
		}
	}
	for _, cmd := range []string{"hook", "list", "do"} {
		if !selfUpgradeTrigger(cmd) {
			t.Errorf("selfUpgradeTrigger(%q) = false, want true", cmd)
		}
	}
}

// TestStaleVersionHintSilentWhenSelfUpgradeHandles pins that the hook
// does not ask the agent to offer an upgrade that flow does by itself.
func TestStaleVersionHintSilentWhenSelfUpgradeHandles(t *testing.T) {
	withFlowRoot(t)
	withSelfUpgradeGates(t, fakeBinary(t))
	withFakeHTTP(t, func(url string) (*http.Response, error) {
		return fakeHTTPResponse(t, 200, `{"tag_name":"v0.2.0"}`), nil
	})
	if got := appendStaleVersionHint(); got != "" {
		t.Errorf("appendStaleVersionHint = %q, want empty", got)
	}
}

// TestStaleVersionHintAfterFailedSelfUpgrade pins that a failed
// automatic upgrade brings the hint back.
func TestStaleVersionHintAfterFailedSelfUpgrade(t *testing.T) {
	root := withFlowRoot(t)
	withSelfUpgradeGates(t, fakeBinary(t))
	withFakeHTTP(t, func(url string) (*http.Response, error) {
		return fakeHTTPResponse(t, 200, `{"tag_name":"v0.2.0"}`), nil
	})
	if err := writeSelfUpgradeState(root, selfUpgradeState{Target: "v0.2.0", Result: "failed", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := appendStaleVersionHint(); !strings.Contains(got, "flow-version-stale: v0.2.0") {
		t.Errorf("appendStaleVersionHint = %q, want the stale hint", got)
	}
}

// TestStaleVersionHintSilentForNewerLocal pins that a binary newer than
// the latest release gets no hint.
func TestStaleVersionHintSilentForNewerLocal(t *testing.T) {
	withFlowRoot(t)
	withVersion(t, "v0.3.0")
	t.Setenv(selfUpgradeEnv, "1")
	withFakeHTTP(t, func(url string) (*http.Response, error) {
		return fakeHTTPResponse(t, 200, `{"tag_name":"v0.2.0"}`), nil
	})
	if got := appendStaleVersionHint(); got != "" {
		t.Errorf("appendStaleVersionHint = %q, want empty", got)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dir, selfUpgradeTempPattern))
	if len(matches) != 0 {
		t.Errorf("temporary downloads left behind: %v", matches)
	}
}
