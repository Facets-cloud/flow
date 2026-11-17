package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeBaseStub writes a minimal base cache file and ages it by `age`.
func writeBaseStub(t *testing.T, age time.Duration) string {
	t.Helper()
	p, err := lookbackCachePath()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBackgroundTrainDue(t *testing.T) {
	initTempFlowRoot(t)
	dir, _ := recallDir()
	os.MkdirAll(dir, 0o755)

	// No base cache yet → due (build it).
	if !backgroundTrainDue() {
		t.Fatal("no base cache should be due")
	}

	// Fresh base (younger than the interval) → not due.
	writeBaseStub(t, time.Hour)
	if backgroundTrainDue() {
		t.Fatal("a fresh base should not be due")
	}

	// Old base, nothing changed since → not due.
	writeBaseStub(t, 48*time.Hour)
	if backgroundTrainDue() {
		t.Fatal("old base with no changes should not be due")
	}

	// Old base + a manifest (deltas recorded by warm calls) → due.
	mp, _ := manifestPath()
	os.WriteFile(mp, []byte(`{"version":1,"deltas":{"x":"f"}}`), 0o644)
	if !backgroundTrainDue() {
		t.Fatal("old base with deltas should be due")
	}
	os.Remove(mp)

	// Old base + flow.db newer than it → due.
	root, _ := flowRoot()
	os.WriteFile(filepath.Join(root, "flow.db"), []byte("db"), 0o644) // now > base mtime
	if !backgroundTrainDue() {
		t.Fatal("old base with a newer flow.db should be due")
	}
}

func TestTrainIntervalSetting(t *testing.T) {
	initTempFlowRoot(t)
	if d := trainInterval(); d != defaultTrainInterval {
		t.Fatalf("default interval = %s, want %s", d, defaultTrainInterval)
	}
	root, _ := flowRoot()
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"recall":{"train_interval":"1h"}}`), 0o644)
	if d := trainInterval(); d != time.Hour {
		t.Fatalf("configured interval = %s, want 1h", d)
	}
	// A malformed value falls back to the default.
	os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"recall":{"train_interval":"not-a-duration"}}`), 0o644)
	if d := trainInterval(); d != defaultTrainInterval {
		t.Fatalf("malformed interval should fall back to %s, got %s", defaultTrainInterval, d)
	}
}

func TestTrainLockSingleFlight(t *testing.T) {
	initTempFlowRoot(t)
	dir, _ := recallDir()
	os.MkdirAll(dir, 0o755)

	if !acquireTrainLock() {
		t.Fatal("first acquire should succeed")
	}
	// A second acquire (this process is alive) is refused.
	if acquireTrainLock() {
		t.Fatal("second acquire should be refused while held")
	}
	releaseTrainLock()
	if !acquireTrainLock() {
		t.Fatal("acquire after release should succeed")
	}
	releaseTrainLock()

	// A stale lock (dead pid) is taken over.
	lp, _ := trainLockPath()
	os.WriteFile(lp, []byte("999999999\n"), 0o644) // a pid that is not alive
	if !acquireTrainLock() {
		t.Fatal("a stale lock (dead pid) should be taken over")
	}
	releaseTrainLock()
}

func TestMaybeLaunchBackgroundTrainGating(t *testing.T) {
	initTempFlowRoot(t)
	dir, _ := recallDir()
	os.MkdirAll(dir, 0o755)

	launched := 0
	old := trainLauncher
	trainLauncher = func() error { launched++; return nil }
	t.Cleanup(func() { trainLauncher = old })

	// Auto run → skipped.
	t.Setenv(autoRunEnvVar, "some-slug")
	maybeLaunchBackgroundTrain()
	if launched != 0 {
		t.Fatal("auto run must skip the background train")
	}
	t.Setenv(autoRunEnvVar, "")

	// Explicit off switch → skipped.
	t.Setenv("FLOW_NO_BG_TRAIN", "1")
	maybeLaunchBackgroundTrain()
	if launched != 0 {
		t.Fatal("FLOW_NO_BG_TRAIN must skip the background train")
	}
	t.Setenv("FLOW_NO_BG_TRAIN", "")

	// Due (no base) → launches once, and the lock is now held so a second
	// call is a no-op.
	maybeLaunchBackgroundTrain()
	if launched != 1 {
		t.Fatalf("expected one launch, got %d", launched)
	}
	maybeLaunchBackgroundTrain()
	if launched != 1 {
		t.Fatalf("second call should be gated by the lock, got %d launches", launched)
	}
}

// TestCmdRecallTrainBGBuildsAndUnlocks runs the detached worker's body against
// a small synthetic corpus: it must write the base cache and release the lock.
func TestCmdRecallTrainBGBuildsAndUnlocks(t *testing.T) {
	matchFixture(t)
	// Pre-hold the lock as the launcher would, then run the worker body.
	if !acquireTrainLock() {
		t.Fatal("could not pre-acquire lock")
	}
	if rc := cmdRecallTrainBG(); rc != 0 {
		t.Fatalf("bg train rc=%d", rc)
	}
	basePath, _ := lookbackCachePath()
	if !matchFileExists(basePath) {
		t.Fatal("background train did not write the base cache")
	}
	lp, _ := trainLockPath()
	if matchFileExists(lp) {
		t.Fatal("background train did not release the lock")
	}
}
