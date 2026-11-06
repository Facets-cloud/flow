package connector

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeKeychain swaps the keychain hooks for an in-memory map (or an
// always-failing keychain when broken) for the test's duration.
func fakeKeychain(t *testing.T, broken bool) map[string]string {
	t.Helper()
	store := map[string]string{}
	set, get, del := KeychainSet, KeychainGet, KeychainDelete
	t.Cleanup(func() { KeychainSet, KeychainGet, KeychainDelete = set, get, del })
	unavailable := errors.New("no keychain")
	KeychainSet = func(service, secret string) error {
		if broken {
			return unavailable
		}
		store[service] = secret
		return nil
	}
	KeychainGet = func(service string) (string, error) {
		if broken {
			return "", unavailable
		}
		s, ok := store[service]
		if !ok {
			return "", ErrNoSecret
		}
		return s, nil
	}
	KeychainDelete = func(service string) error {
		if broken {
			return unavailable
		}
		delete(store, service)
		return nil
	}
	return store
}

func TestSecretStoreKeychain(t *testing.T) {
	kc := fakeKeychain(t, false)
	s := SecretStore{FileDir: filepath.Join(t.TempDir(), "secrets")}
	where, err := s.Put("desk", "s3cret")
	if err != nil || where != "keychain" {
		t.Fatalf("Put = %q, %v", where, err)
	}
	if kc["flow-connector-desk"] != "s3cret" {
		t.Errorf("keychain service name: %v", kc)
	}
	if _, err := os.Stat(filepath.Join(s.FileDir, "desk")); !os.IsNotExist(err) {
		t.Errorf("secret also written to a file")
	}
	if got, err := s.Get("desk"); err != nil || got != "s3cret" {
		t.Errorf("Get = %q, %v", got, err)
	}
	if s.Where("desk") != "keychain" {
		t.Errorf("Where = %q", s.Where("desk"))
	}
	if err := s.Delete("desk"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("desk"); !errors.Is(err, ErrNoSecret) {
		t.Errorf("after Delete Get err = %v", err)
	}
}

func TestSecretStoreFileFallback(t *testing.T) {
	fakeKeychain(t, true)
	s := SecretStore{FileDir: filepath.Join(t.TempDir(), "secrets")}
	where, err := s.Put("desk", "s3cret")
	if err != nil || where != "file" {
		t.Fatalf("Put = %q, %v", where, err)
	}
	fi, err := os.Stat(filepath.Join(s.FileDir, "desk"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode: %v %v", fi, err)
	}
	if got, err := s.Get("desk"); err != nil || got != "s3cret" {
		t.Errorf("Get = %q, %v", got, err)
	}
	if s.Where("desk") != "file" {
		t.Errorf("Where = %q", s.Where("desk"))
	}
	if err := s.Delete("desk"); err != nil {
		t.Fatal(err)
	}
	if s.Where("desk") != "" {
		t.Errorf("secret survived Delete")
	}
}

func TestNewSecretIsRandom(t *testing.T) {
	a, b := NewSecret(), NewSecret()
	if len(a) < 32 || a == b {
		t.Errorf("secrets %q %q", a, b)
	}
}

func TestRunPassesVerbStdinEnvAndTimesOut(t *testing.T) {
	dir := writeConnector(t, t.TempDir(), "desk", good)
	out := filepath.Join(dir, "got")
	script := "#!/bin/sh\necho \"$1 $FLOW_CONNECTOR_TOKEN\" > " + out + "\ncat >> " + out + "\n"
	if err := os.WriteFile(filepath.Join(dir, "run"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(m, "send", []byte(`{"id":"x"}`), []string{"FLOW_CONNECTOR_TOKEN=tok"}, 5*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "send tok\n{\"id\":\"x\"}" {
		t.Errorf("connector saw %q", got)
	}

	// Non-zero exit is an error carrying stderr.
	if err := os.WriteFile(filepath.Join(dir, "run"), []byte("#!/bin/sh\necho nope >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Run(m, "send", nil, nil, 5*time.Second); err == nil || !contains(err.Error(), "nope") {
		t.Errorf("failing run err = %v", err)
	}

	// A hanging connector (and the child it forked) is killed on timeout.
	if err := os.WriteFile(filepath.Join(dir, "run"), []byte("#!/bin/sh\nsleep 30 &\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = Run(m, "send", nil, nil, 300*time.Millisecond)
	if err == nil || !contains(err.Error(), "timed out") {
		t.Errorf("hang err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("timeout took %v", d)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
