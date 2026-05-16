package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreferredUIFlowBinaryUsesNearestWorktreeBin(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	nested := filepath.Join(worktree, "internal", "server")
	if err := os.MkdirAll(filepath.Join(worktree, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	localFlow := filepath.Join(worktree, "bin", "flow")
	if err := os.WriteFile(localFlow, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(root, "usr", "local", "bin", "flow")
	if err := os.MkdirAll(filepath.Dir(fallback), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fallback, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	got := preferredUIFlowBinary(fallback)
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(localFlow)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("preferredUIFlowBinary() = %q, want %q", got, localFlow)
	}
}

func TestPreferredUIFlowBinaryHonorsExecutableOverride(t *testing.T) {
	root := t.TempDir()
	override := filepath.Join(root, "custom-flow")
	if err := os.WriteFile(override, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(root, "fallback-flow")
	if err := os.WriteFile(fallback, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOW_UI_FLOW_BIN", override)

	if got := preferredUIFlowBinary(fallback); got != override {
		t.Fatalf("preferredUIFlowBinary() = %q, want %q", got, override)
	}
}
