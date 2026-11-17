package app

import (
	"flow/internal/match"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Background recall-index training. The shipped SessionStart hook (which
// already fires on every session start/resume) opportunistically kicks off a
// fully-detached `flow recall train` so the LSA basis and the compacted base
// index stay fresh WITHOUT a warm recall call ever paying for a refit. It:
//
//   - runs only if the last train is older than recall.train_interval
//     (default 24h) AND something has changed since;
//   - is single-flight via a lock file (a second session start is a no-op);
//   - returns immediately (the hook never blocks on it);
//   - runs niced and under a soft memory limit;
//   - logs to $FLOW_ROOT/match/train.log and never surfaces an error;
//   - is skipped inside headless auto runs (nobody is watching, and the
//     run has its own work to do).
//
// No new hook and no hook command-string change — it rides the existing
// SessionStart handler.

const (
	defaultTrainInterval = 24 * time.Hour
	// bgTrainMemLimit is the soft memory ceiling for the detached train (a
	// full build that hydrates transcripts + fits an SVD). Overridable via
	// GOMEMLIMIT, which the Go runtime reads directly.
	bgTrainMemLimit = 1500 << 20 // 1.5 GiB
)

// trainLockPath / trainLogPath live under the recall dir.
func trainLockPath() (string, error) {
	dir, err := recallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "train.lock"), nil
}

func trainLogPath() (string, error) {
	dir, err := recallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "train.log"), nil
}

// trainInterval reads recall.train_interval (a duration string) with the
// 24h default. A malformed value falls back to the default.
func trainInterval() time.Duration {
	if v, ok := settingGet(loadSettings(), "recall.train_interval"); ok {
		if s, ok := v.(string); ok {
			if d, err := time.ParseDuration(s); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultTrainInterval
}

// maybeLaunchBackgroundTrain is called from the SessionStart hook. It is
// best-effort and MUST NOT block or error: every failure path is a silent
// no-op so a training hiccup can never disturb the session.
func maybeLaunchBackgroundTrain() {
	defer func() { _ = recover() }() // a hook must never crash
	if os.Getenv(autoRunEnvVar) != "" {
		return // headless auto run — skip
	}
	if os.Getenv("FLOW_NO_BG_TRAIN") != "" {
		return // escape hatch for tests / users who want it off
	}
	if !backgroundTrainDue() {
		return
	}
	if !acquireTrainLock() {
		return // another train is in flight
	}
	// From here the lock is held; the detached worker releases it. If we fail
	// to launch, release it ourselves so a later start can retry.
	if err := launchBackgroundTrain(); err != nil {
		releaseTrainLock()
	}
}

// backgroundTrainDue reports whether a train should run: no base cache yet, or
// the base is older than the interval AND something changed since it was
// written. "Something changed" is a cheap proxy — the flow.db mtime (task
// mutations) or the presence of per-task deltas (warm calls recorded drift).
func backgroundTrainDue() bool {
	basePath, err := lookbackCachePath()
	if err != nil {
		return false
	}
	fi, err := os.Stat(basePath)
	if err != nil {
		return true // never trained — build it
	}
	if time.Since(fi.ModTime()) < trainInterval() {
		return false // trained recently enough
	}
	baseMod := fi.ModTime()
	// Deltas present → warm calls saw changes since the base was written.
	if mp, err := manifestPath(); err == nil {
		if _, err := os.Stat(mp); err == nil {
			return true
		}
	}
	// flow.db newer than the base → task rows changed since.
	if dbPath, err := flowDBPath(); err == nil {
		if dfi, err := os.Stat(dbPath); err == nil && dfi.ModTime().After(baseMod) {
			return true
		}
	}
	return false
}

// trainLauncher starts the detached background-train worker. Overridable in
// tests so no real process is spawned.
var trainLauncher = func() error {
	// Under `go test` the executable is the package test binary, not `flow`;
	// spawning it with a hidden subcommand would re-run the suite. A test that
	// wants to exercise the launch overrides this var. Otherwise no-op.
	if runningUnderTest() {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logPath, err := trainLogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logF.Close()
	cmd := exec.Command(self, "__recall-train-bg")
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from the session
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

func launchBackgroundTrain() error { return trainLauncher() }

// runningUnderTest reports whether the process is a `go test` binary (its name
// ends in ".test"), so the default launcher can avoid spawning itself.
func runningUnderTest() bool {
	return len(os.Args) > 0 && strings.HasSuffix(os.Args[0], ".test")
}

// cmdRecallTrainBG is the hidden detached worker (`flow __recall-train-bg`).
// It nices itself, sets a soft memory limit, rebuilds the index, and always
// releases the lock. Output is already redirected to train.log by the
// launcher. It never returns a non-zero code that anyone reads.
func cmdRecallTrainBG() int {
	defer releaseTrainLock()
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 15) // low priority (nice)
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(bgTrainMemLimit)
	}
	fmt.Fprintf(os.Stderr, "%s background train start (pid %d)\n", time.Now().Format(time.RFC3339), os.Getpid())
	rows, err := matchScan()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s background train scan error: %v\n", time.Now().Format(time.RFC3339), err)
		return 0 // never surface
	}
	tasks := hydrateAll(rows)
	space := match.FitRelatedSpace(tasks)
	if err := saveLookbackCache(rows, tasks, space); err != nil {
		fmt.Fprintf(os.Stderr, "%s background train write error: %v\n", time.Now().Format(time.RFC3339), err)
		return 0
	}
	fmt.Fprintf(os.Stderr, "%s background train done: %d tasks\n", time.Now().Format(time.RFC3339), len(tasks))
	return 0
}

// ---- single-flight lock ----

// acquireTrainLock creates the lock exclusively, writing this process's pid.
// A lock held by a dead pid is treated as stale and taken over. Returns false
// when a live train already holds it.
func acquireTrainLock() bool {
	path, err := trainLockPath()
	if err != nil {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintf(f, "%d\n", os.Getpid())
		f.Close()
		return true
	}
	// Lock exists: is its owner still alive?
	if b, rerr := os.ReadFile(path); rerr == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && processAlive(pid) {
			return false // a live train holds it
		}
	}
	// Stale lock — take it over.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return false
	}
	return true
}

func releaseTrainLock() {
	if path, err := trainLockPath(); err == nil {
		os.Remove(path)
	}
}
