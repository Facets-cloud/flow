package connector

import (
	"bytes"
	"crypto/rand"
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
// human. Secrets live in the macOS keychain (service
// flow-connector-<name>) or, where that is unavailable, in a 0600 file
// under $FLOW_ROOT/secrets/ — never in flow.db or the connector folder.

// ErrNoSecret means no secret is stored for a connector.
var ErrNoSecret = errors.New("no connector secret stored")

const keychainAccount = "flow"

// KeychainService is the keychain service name for a connector's secret.
func KeychainService(name string) string { return "flow-connector-" + name }

// Keychain hooks — package vars so tests never touch the real keychain.
var (
	KeychainSet = func(service, secret string) error {
		if err := keychainAvailable(); err != nil {
			return err
		}
		return exec.Command("security", "add-generic-password", "-U",
			"-a", keychainAccount, "-s", service, "-w", secret).Run()
	}
	KeychainGet = func(service string) (string, error) {
		if err := keychainAvailable(); err != nil {
			return "", err
		}
		var out bytes.Buffer
		cmd := exec.Command("security", "find-generic-password", "-a", keychainAccount, "-s", service, "-w")
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return "", ErrNoSecret
		}
		return strings.TrimRight(out.String(), "\n"), nil
	}
	KeychainDelete = func(service string) error {
		if err := keychainAvailable(); err != nil {
			return err
		}
		_ = exec.Command("security", "delete-generic-password", "-a", keychainAccount, "-s", service).Run()
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

// NewSecret returns a fresh random 256-bit secret, hex encoded.
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)
}

// SecretStore stores connector secrets in the keychain, falling back to
// 0600 files in FileDir ($FLOW_ROOT/secrets).
type SecretStore struct {
	FileDir string
}

func (s SecretStore) file(name string) string { return filepath.Join(s.FileDir, name) }

// Put stores a secret and reports where: "keychain" or "file".
func (s SecretStore) Put(name, secret string) (string, error) {
	if err := KeychainSet(KeychainService(name), secret); err == nil {
		return "keychain", nil
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
	if v, err := KeychainGet(KeychainService(name)); err == nil && v != "" {
		return v, nil
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
	if v, err := KeychainGet(KeychainService(name)); err == nil && v != "" {
		return "keychain"
	}
	if _, err := os.Stat(s.file(name)); err == nil {
		return "file"
	}
	return ""
}

// Delete removes a connector's secret from both places.
func (s SecretStore) Delete(name string) error {
	_ = KeychainDelete(KeychainService(name))
	if err := os.Remove(s.file(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
