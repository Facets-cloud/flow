package connector

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Each installed connector has its own random secret. flow hands it to
// the connector process as FLOW_CONNECTOR_TOKEN; `flow human reply|seen`
// require it, so an agent (which never has it) cannot answer for the
// human. Secrets live in the macOS keychain or, where that is
// unavailable, in a 0600 file under $FLOW_ROOT/secrets/ — never in
// flow.db or the connector folder.
//
// A keychain item is keyed by (service, account):
//
//	service  flow-connector-<name>        which connector
//	account  flow-<12 hex of sha256(root)> which flow home (KeychainAccount)
//
// so two flow homes on one Mac (two FLOW_ROOTs) never share or overwrite
// each other's secret for the same connector name.

// ErrNoSecret means no secret is stored for a connector.
var ErrNoSecret = errors.New("no connector secret stored")

// KeychainService is the keychain service name for a connector's secret.
func KeychainService(name string) string { return "flow-connector-" + name }

// KeychainAccount is the keychain account scoping secrets to one flow
// home: "flow-" + the first 12 hex chars of sha256 of the absolute,
// symlink-resolved, cleaned flow root. Deterministic per root.
func KeychainAccount(flowRoot string) string {
	p := flowRoot
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	sum := sha256.Sum256([]byte(filepath.Clean(p)))
	return "flow-" + hex.EncodeToString(sum[:])[:12]
}

// Keychain hooks — package vars so tests never touch the real keychain.
var (
	KeychainSet = func(service, account, secret string) error {
		if err := keychainAvailable(); err != nil {
			return err
		}
		return exec.Command("security", "add-generic-password", "-U",
			"-a", account, "-s", service, "-w", secret).Run()
	}
	KeychainGet = func(service, account string) (string, error) {
		if err := keychainAvailable(); err != nil {
			return "", err
		}
		var out bytes.Buffer
		cmd := exec.Command("security", "find-generic-password", "-a", account, "-s", service, "-w")
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return "", ErrNoSecret
		}
		return strings.TrimRight(out.String(), "\n"), nil
	}
	KeychainDelete = func(service, account string) error {
		if err := keychainAvailable(); err != nil {
			return err
		}
		_ = exec.Command("security", "delete-generic-password", "-a", account, "-s", service).Run()
		return nil
	}
)

func keychainAvailable() error {
	if runtime.GOOS != "darwin" {
		return errors.New("keychain is macOS-only")
	}
	if _, err := exec.LookPath("security"); err != nil {
		return errors.New("security(1) not found")
	}
	return nil
}

// SecretStoreEnv selects the secret backend. "file" (TESTING ONLY) skips
// the keychain entirely and keeps every secret in a 0600 file under
// $FLOW_ROOT/secrets — so real-binary tests and the peer-local sample can
// run on macOS without touching the user's keychain. Unset (or any other
// value) is the normal keychain-then-file behavior.
const SecretStoreEnv = "FLOW_SECRET_STORE"

func fileOnly() bool { return os.Getenv(SecretStoreEnv) == "file" }

// NewSecret returns a fresh random 256-bit secret, hex encoded.
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)
}

// SecretStore stores connector secrets in the keychain, falling back to
// 0600 files in FileDir ($FLOW_ROOT/secrets). Account is the keychain
// account for this flow home (KeychainAccount of the flow root).
type SecretStore struct {
	FileDir string
	Account string
}

func (s SecretStore) file(name string) string { return filepath.Join(s.FileDir, name) }

// Put stores a secret and reports where: "keychain" or "file".
func (s SecretStore) Put(name, secret string) (string, error) {
	if !fileOnly() {
		if err := KeychainSet(KeychainService(name), s.Account, secret); err == nil {
			return "keychain", nil
		}
	}
	if err := os.MkdirAll(s.FileDir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(s.file(name), []byte(secret), 0o600); err != nil {
		return "", err
	}
	return "file", os.Chmod(s.file(name), 0o600)
}

// Get returns a connector's secret (keychain first, then the file).
func (s SecretStore) Get(name string) (string, error) {
	if !fileOnly() {
		if v, err := KeychainGet(KeychainService(name), s.Account); err == nil && v != "" {
			return v, nil
		}
	}
	data, err := os.ReadFile(s.file(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNoSecret
		}
		return "", err
	}
	if v := strings.TrimSpace(string(data)); v != "" {
		return v, nil
	}
	return "", ErrNoSecret
}

// Where reports where a connector's secret lives: "keychain", "file", or
// "" when none is stored.
func (s SecretStore) Where(name string) string {
	if !fileOnly() {
		if v, err := KeychainGet(KeychainService(name), s.Account); err == nil && v != "" {
			return "keychain"
		}
	}
	if _, err := os.Stat(s.file(name)); err == nil {
		return "file"
	}
	return ""
}

// Delete removes a connector's secret from both places.
func (s SecretStore) Delete(name string) error {
	if !fileOnly() {
		_ = KeychainDelete(KeychainService(name), s.Account)
	}
	if err := os.Remove(s.file(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
