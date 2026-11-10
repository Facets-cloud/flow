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
	KeychainSet = func(service, account, secret string) error {
		if broken {
			return unavailable
		}
		store[service+"@"+account] = secret
		return nil
	}
	KeychainGet = func(service, account string) (string, error) {
		if broken {
			return "", unavailable
		}
		s, ok := store[service+"@"+account]
		if !ok {
			return "", ErrNoSecret
		}
		return s, nil
	}
	KeychainDelete = func(service, account string) error {
		if broken {
			return unavailable
		}
		delete(store, service+"@"+account)
		return nil
	}
	return store
}

func TestSecretStoreKeychain(t *testing.T) {
	kc := fakeKeychain(t, false)
	s := SecretStore{FileDir: filepath.Join(t.TempDir(), "secrets"), Account: "flow-abc"}
	where, err := s.Put("desk", "s3cret")
	if err != nil || where != "keychain" {
		t.Fatalf("Put = %q, %v", where, err)
	}
	if kc["flow-connector-desk@flow-abc"] != "s3cret" {
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

// Two flow homes on one Mac must not share or overwrite a connector's
// keychain secret: the account is derived from the resolved FLOW_ROOT.
func TestKeychainAccountIsPerFlowRoot(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	accA, accB := KeychainAccount(a), KeychainAccount(b)
	if accA == accB {
		t.Fatalf("distinct roots share account %q", accA)
	}
	if !strings.HasPrefix(accA, "flow-") || len(accA) != len("flow-")+12 {
		t.Errorf("account %q, want flow-<12 hex>", accA)
	}
	if KeychainAccount(link) != accA || KeychainAccount(a+"/.") != accA {
		t.Errorf("symlinked/unclean root changes the account")
	}
	if KeychainAccount(a) != accA {
		t.Errorf("account is not deterministic")
	}

	kc := fakeKeychain(t, false)
	sa := SecretStore{FileDir: filepath.Join(a, "secrets"), Account: accA}
	sb := SecretStore{FileDir: filepath.Join(b, "secrets"), Account: accB}
	if _, err := sa.Put("macos", "secret-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Put("macos", "secret-b"); err != nil {
		t.Fatal(err)
	}
	if len(kc) != 2 {
		t.Fatalf("want 2 keychain entries, got %v", kc)
	}
	if got, _ := sa.Get("macos"); got != "secret-a" {
		t.Errorf("home A reads %q", got)
	}
	if err := sb.Delete("macos"); err != nil {
		t.Fatal(err)
	}
	if got, err := sa.Get("macos"); err != nil || got != "secret-a" {
		t.Errorf("deleting in home B touched home A: %q %v", got, err)
	}
	if sb.Where("macos") != "" {
		t.Errorf("home B secret survived delete")
	}
}

// FLOW_SECRET_STORE=file (testing only) never touches the keychain, even
// when one is available: secrets live in 0600 files under FileDir.
func TestSecretStoreFileSeam(t *testing.T) {
	kc := fakeKeychain(t, false)
	t.Setenv(SecretStoreEnv, "file")
	s := SecretStore{FileDir: filepath.Join(t.TempDir(), "secrets"), Account: "flow-abc"}
	kc["flow-connector-desk@flow-abc"] = "from-keychain"
	where, err := s.Put("desk", "s3cret")
	if err != nil || where != "file" {
		t.Fatalf("Put = %q, %v", where, err)
	}
	if kc["flow-connector-desk@flow-abc"] != "from-keychain" {
		t.Errorf("keychain written under the file seam")
	}
	if got, err := s.Get("desk"); err != nil || got != "s3cret" {
		t.Errorf("Get = %q, %v (keychain must be skipped)", got, err)
	}
	if w := s.Where("desk"); w != "file" {
		t.Errorf("Where = %q", w)
	}
	if err := s.Delete("desk"); err != nil {
		t.Fatal(err)
	}
	if _, ok := kc["flow-connector-desk@flow-abc"]; !ok {
		t.Errorf("Delete reached the keychain under the file seam")
	}
	if w := s.Where("desk"); w != "" {
		t.Errorf("Where after delete = %q", w)
	}
}
