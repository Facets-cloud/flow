package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/mod/semver"
)

// flow upgrades itself to the latest GitHub release. The SessionStart
// hook caches the latest tag at most once a day (release.go). After a
// command, flow reads only that cache. When the cache names a newer
// release, flow starts a detached `flow __self-upgrade` and does not
// wait for it. That process downloads the build for this Mac, checks
// its SHA-256 against the release, and renames it over the binary. It
// never writes into the running file: on Apple silicon, a write into a
// signed binary gets the next runs killed. The next flow run sees the
// new Version and refreshes the skill (maybeAutoUpgradeSkill).
const (
	selfUpgradeArg = "__self-upgrade"
	// selfUpgradeEnv stops the automatic upgrade. `flow upgrade` and the
	// SessionStart stale-version hint stay.
	selfUpgradeEnv       = "FLOW_NO_AUTO_UPGRADE"
	selfUpgradeStateFile = ".self-upgrade.json"
	selfUpgradeClaimFile = ".self-upgrade.claim"
	selfUpgradeLogFile   = "self-upgrade.log"
	// selfUpgradeClaimTTL frees the claim of an upgrade that died.
	selfUpgradeClaimTTL = time.Hour
	// selfUpgradeRetryAfter is the pause after a failed attempt at one target.
	selfUpgradeRetryAfter = 24 * time.Hour
	// selfUpgradeTimeout bounds each HTTP call of an upgrade.
	selfUpgradeTimeout = 5 * time.Minute
	// selfUpgradeTempPattern names the download beside the binary.
	selfUpgradeTempPattern = ".flow-upgrade-*"
)

// releaseTagRe matches a release tag such as v0.1.0-alpha.29. A
// `git describe` build (v0.1.0-alpha.24-1-g7ad6e4e) does not match,
// because a pre-release part of a tag never holds a hyphen.
var releaseTagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$`)

// describeSuffixRe matches the commits-since-tag suffix that
// `git describe` adds to a source build.
var describeSuffixRe = regexp.MustCompile(`-\d+-g[0-9a-f]+(-dirty)?$`)

// selfUpgradeState is the result of the last upgrade attempt, at
// ~/.flow/.self-upgrade.json.
type selfUpgradeState struct {
	From   string    `json:"from,omitempty"`
	Target string    `json:"target"`
	Result string    `json:"result"` // "upgraded" or "failed"
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
}

// githubRelease is the part of the GitHub release JSON that an upgrade uses.
type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
		URL    string `json:"browser_download_url"`
	} `json:"assets"`
}

// Seams for tests.
var (
	selfUpgradeExec   = resolvedSelf
	startSelfUpgrade  = startDetachedSelfUpgrade
	selfUpgradeAsRoot = func() bool { return os.Geteuid() == 0 }
	selfUpgradeGOOS   = runtime.GOOS
	// httpGetForUpgrade fetches the release JSON and the binary.
	httpGetForUpgrade = func(url string) (*http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), selfUpgradeTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			return nil, err
		}
		resp.Body = cancelOnClose{resp.Body, cancel}
		return resp, nil
	}
)

// cancelOnClose ends the request context when the body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// isNewerRelease reports whether latest is a newer release than
// current. A source build compares as the tag it was built from.
func isNewerRelease(latest, current string) bool {
	current = describeSuffixRe.ReplaceAllString(current, "")
	if !semver.IsValid(latest) || !semver.IsValid(current) {
		return false
	}
	return semver.Compare(latest, current) > 0
}

// selfUpgradeOffReason returns why this flow does not upgrade itself,
// or "" when it does. execPath is the resolved binary.
func selfUpgradeOffReason(execPath string) string {
	switch {
	case !releaseTagRe.MatchString(Version):
		// `make install` and source builds belong to a developer.
		return "dev"
	case os.Getenv(selfUpgradeEnv) != "":
		return "env"
	case os.Getenv("CI") != "":
		// A CI job pins its version.
		return "ci"
	case selfUpgradeGOOS != "darwin":
		// Releases hold macOS builds only.
		return "platform"
	case selfUpgradeAsRoot():
		// sudo keeps HOME on macOS, so root would own the files it writes there.
		return "root"
	case execPath == "" || !folderWritable(filepath.Dir(execPath)):
		return "folder"
	}
	return ""
}

// selfUpgradeTrigger reports whether cmd may start an automatic upgrade.
func selfUpgradeTrigger(cmd string) bool {
	switch cmd {
	case "__auto-exec", "__owner-tick", selfUpgradeArg, "upgrade",
		"--version", "-v", "version", "-h", "--help", "help":
		return false
	}
	return true
}

// maybeSelfUpgrade starts the detached upgrade when the version cache
// names a newer release and every gate passes. It reads no network and
// never blocks the command. All errors are silent.
func maybeSelfUpgrade() {
	root, err := flowRoot()
	if err != nil {
		return
	}
	latest, ok := readCachedVersion(filepath.Join(root, ".version-cache.json"))
	if !ok || !isNewerRelease(latest, Version) {
		return
	}
	execPath := selfUpgradeExec()
	if selfUpgradeOffReason(execPath) != "" {
		return
	}
	if st, err := readSelfUpgradeState(root); err == nil && st.Result == "failed" &&
		st.Target == latest && nowForVersion().Sub(st.At) < selfUpgradeRetryAfter {
		return
	}
	claim := filepath.Join(root, selfUpgradeClaimFile)
	if !takeClaim(claim, nowForVersion()) {
		return
	}
	if err := startSelfUpgrade(execPath, root); err != nil {
		os.Remove(claim)
	}
}

// selfUpgradeHandles reports whether the automatic upgrade takes care
// of latest, so the SessionStart hook need not ask the agent to offer
// an upgrade.
func selfUpgradeHandles(latest string) bool {
	if selfUpgradeOffReason(selfUpgradeExec()) != "" {
		return false
	}
	root, err := flowRoot()
	if err != nil {
		return false
	}
	st, err := readSelfUpgradeState(root)
	return err != nil || st.Result != "failed" || st.Target != latest
}

// takeClaim creates the claim file. A claim older than
// selfUpgradeClaimTTL belongs to an upgrade that died, so it is taken over.
func takeClaim(path string, now time.Time) bool {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		info, serr := os.Stat(path)
		if serr != nil || now.Sub(info.ModTime()) < selfUpgradeClaimTTL {
			return false
		}
		os.Remove(path)
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// startDetachedSelfUpgrade starts `flow __self-upgrade` from execPath in
// its own session, with its output in the log file, and does not wait.
func startDetachedSelfUpgrade(execPath, root string) error {
	if testing.Testing() {
		// A test binary must never replace itself.
		return errors.New("self-upgrade is off in tests")
	}
	logF, err := os.Create(filepath.Join(root, selfUpgradeLogFile))
	if err != nil {
		return err
	}
	defer logF.Close()
	c := exec.Command(execPath, selfUpgradeArg)
	c.Stdout, c.Stderr = logF, logF
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

// cmdSelfUpgrade is the detached flow. It runs from the binary that is
// at the path now: when an earlier upgrade already replaced that file,
// it is the new version and has nothing to do.
func cmdSelfUpgrade() int {
	root, err := flowRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer os.Remove(filepath.Join(root, selfUpgradeClaimFile))

	rel, err := fetchLatestRelease()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !isNewerRelease(rel.TagName, Version) {
		fmt.Printf("flow %s is not older than %s; nothing to do\n", Version, rel.TagName)
		return 0
	}
	err = replaceSelf(selfUpgradeExec(), rel)
	st := selfUpgradeState{From: Version, Target: rel.TagName, Result: "upgraded", At: nowForVersion().UTC()}
	if err != nil {
		st.Result, st.Error = "failed", err.Error()
	}
	if serr := writeSelfUpgradeState(root, st); err == nil {
		err = serr
	}
	fmt.Printf("flow %s → %s: %s\n", st.From, st.Target, st.Result)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// cmdUpgrade is `flow upgrade [--check] [--force]`: replace this flow
// with the latest release now.
func cmdUpgrade(args []string) int {
	fs := flagSet("upgrade")
	check := fs.Bool("check", false, "only report whether a newer release exists")
	force := fs.Bool("force", false, "replace a development or source build too")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rel, err := fetchLatestRelease()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	dev := !releaseTagRe.MatchString(Version)
	if !dev && !isNewerRelease(rel.TagName, Version) {
		fmt.Printf("flow is up to date (%s)\n", Version)
		return 0
	}
	if *check {
		fmt.Printf("flow %s is available (this is %s). Run `flow upgrade` to install it.\n", rel.TagName, Version)
		return 0
	}
	if dev && !*force {
		fmt.Fprintf(os.Stderr, "error: this flow is a development build (%s). Run `flow upgrade --force` to replace it with %s.\n", Version, rel.TagName)
		return 1
	}
	execPath := selfUpgradeExec()
	if execPath == "" || !folderWritable(filepath.Dir(execPath)) {
		fmt.Fprintf(os.Stderr, "error: flow cannot write to the folder of %s. Install flow in a folder you own, such as ~/.local/bin.\n", execPath)
		return 1
	}
	if err := replaceSelf(execPath, rel); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("flow %s → %s: upgraded %s\n", Version, rel.TagName, execPath)
	fmt.Println("The next flow command refreshes the skill and the hooks.")
	return 0
}

// fetchLatestRelease reads the latest release from GitHub. It never
// uses the version cache.
func fetchLatestRelease() (*githubRelease, error) {
	resp, err := httpGetForUpgrade(githubLatestReleaseURL)
	if err != nil {
		return nil, fmt.Errorf("read the latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read the latest release: GitHub returned HTTP %d", resp.StatusCode)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("read the latest release: %w", err)
	}
	if !semver.IsValid(rel.TagName) {
		return nil, fmt.Errorf("the latest release has the tag %q, which is not a version", rel.TagName)
	}
	return &rel, nil
}

// replaceSelf downloads the build of rel for this Mac beside execPath,
// checks its SHA-256 and renames it over execPath.
func replaceSelf(execPath string, rel *githubRelease) error {
	url, want, err := assetFor(rel)
	if err != nil {
		return err
	}
	info, err := os.Stat(execPath)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(execPath), selfUpgradeTempPattern)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // a no-op after the rename
	h := sha256.New()
	err = downloadTo(url, io.MultiWriter(tmp, h))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("the download has SHA-256 %s, but release %s gives %s", got, rel.TagName, want)
	}
	if err := os.Chmod(tmpPath, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmpPath, execPath)
}

// assetFor returns the download URL and the SHA-256 of the build of rel
// for this Mac.
func assetFor(rel *githubRelease) (url, sha string, err error) {
	name := "flow-" + runtime.GOOS + "-" + runtime.GOARCH
	for _, a := range rel.Assets {
		if a.Name != name {
			continue
		}
		sha, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok || sha == "" {
			return "", "", fmt.Errorf("release %s gives no SHA-256 for %s", rel.TagName, name)
		}
		return a.URL, sha, nil
	}
	return "", "", fmt.Errorf("release %s has no %s build", rel.TagName, name)
}

// downloadTo writes the body at url to w.
func downloadTo(url string, w io.Writer) error {
	resp, err := httpGetForUpgrade(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// resolvedSelf returns the path of the running binary, symlinks resolved.
func resolvedSelf() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// folderWritable reports whether a file can be created in dir.
func folderWritable(dir string) bool {
	f, err := os.CreateTemp(dir, selfUpgradeTempPattern)
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func readSelfUpgradeState(root string) (selfUpgradeState, error) {
	var st selfUpgradeState
	raw, err := os.ReadFile(filepath.Join(root, selfUpgradeStateFile))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(raw, &st)
}

func writeSelfUpgradeState(root string, st selfUpgradeState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, selfUpgradeStateFile), raw, 0o644)
}
