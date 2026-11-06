package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConnector makes a connector folder with the given manifest JSON and
// an executable ./run.
func writeConnector(t *testing.T, parent, dirName, manifest string) string {
	t.Helper()
	dir := filepath.Join(parent, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

const good = `{"name":"desk","contract":1,"exec":"./run","capabilities":["send"],"events":["ask","urgent"]}`

func TestLoadValidManifest(t *testing.T) {
	dir := writeConnector(t, t.TempDir(), "desk", good)
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Name != "desk" || m.Contract != 1 || m.Dir != dir {
		t.Errorf("manifest = %+v", m)
	}
	if m.ExecPath() != filepath.Join(dir, "run") {
		t.Errorf("exec path %s", m.ExecPath())
	}
	if !m.Wants("ask") || !m.Wants("urgent") || m.Wants("fyi") {
		t.Errorf("Wants wrong for %v", m.Events)
	}
}

func TestLoadRejectsBadManifests(t *testing.T) {
	cases := map[string]string{
		"bad json":      `{`,
		"no name":       `{"contract":1,"exec":"./run","capabilities":["send"],"events":["ask"]}`,
		"bad name":      `{"name":"Desk Top","contract":1,"exec":"./run","capabilities":["send"],"events":["ask"]}`,
		"contract 2":    `{"name":"d","contract":2,"exec":"./run","capabilities":["send"],"events":["ask"]}`,
		"no exec":       `{"name":"d","contract":1,"capabilities":["send"],"events":["ask"]}`,
		"abs exec":      `{"name":"d","contract":1,"exec":"/bin/sh","capabilities":["send"],"events":["ask"]}`,
		"escaping exec": `{"name":"d","contract":1,"exec":"../run","capabilities":["send"],"events":["ask"]}`,
		"missing exec":  `{"name":"d","contract":1,"exec":"./nope","capabilities":["send"],"events":["ask"]}`,
		"no send":       `{"name":"d","contract":1,"exec":"./run","capabilities":["listen"],"events":["ask"]}`,
		"no events":     `{"name":"d","contract":1,"exec":"./run","capabilities":["send"],"events":[]}`,
		"unknown event": `{"name":"d","contract":1,"exec":"./run","capabilities":["send"],"events":["ask","broadcast"]}`,
	}
	for name, manifest := range cases {
		dir := writeConnector(t, t.TempDir(), "d", manifest)
		if _, err := Load(dir); err == nil {
			t.Errorf("%s: Load accepted it", name)
		}
	}
	// A non-executable exec is rejected.
	dir := writeConnector(t, t.TempDir(), "desk", good)
	if err := os.Chmod(filepath.Join(dir, "run"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "executable") {
		t.Errorf("non-executable exec: err=%v", err)
	}
}

func TestListInstalled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "connectors")
	if got, errs := List(root); len(got) != 0 || len(errs) != 0 {
		t.Fatalf("missing root: %v %v", got, errs)
	}
	writeConnector(t, root, "desk", good)
	writeConnector(t, root, "zed", strings.Replace(good, `"desk"`, `"zed"`, 1))
	writeConnector(t, root, "wrongdir", good) // folder name must equal manifest name
	if err := os.WriteFile(filepath.Join(root, "dispatch.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, errs := List(root)
	if len(got) != 2 || got[0].Name != "desk" || got[1].Name != "zed" {
		t.Errorf("List = %v", got)
	}
	if len(errs) != 1 {
		t.Errorf("want 1 error (wrongdir), got %v", errs)
	}
}
