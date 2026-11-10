package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/connector"
)

// P9: the peer-local sample (examples/connectors/peer-local) — role
// "peer", stock POSIX tools only. Its send runs the OTHER home's flow
// `peer deliver` with that home's peer-local token. Here FLOW_BIN is a
// fake that records argv and env, so the JSON handling is pinned without
// a real flow.

const peerLocalSample = "../../examples/connectors/peer-local"

func TestPeerLocalSampleManifest(t *testing.T) {
	m, err := connector.Load(peerLocalSample)
	if err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	if m.Name != "peer-local" || !m.HasRole(connector.RolePeer) || m.HasRole(connector.RoleHuman) {
		t.Errorf("manifest = %+v", m)
	}
	for _, n := range availableSamples() {
		if n == "peer-local" {
			return
		}
	}
	t.Errorf("peer-local is not embedded: %v", availableSamples())
}

type peerLocalRun struct {
	args []string
	env  map[string]string
	out  string
}

// runPeerLocal runs the sample's `send` with payload; the other home
// (at) holds secrets/peer-local = "tok-b".
func runPeerLocal(t *testing.T, payloadFor func(at string) string) (peerLocalRun, error) {
	t.Helper()
	dir := t.TempDir()
	other := filepath.Join(dir, "home-b")
	if err := os.MkdirAll(filepath.Join(other, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "flow.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "secrets", "peer-local"), []byte("tok-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "flow")
	script := "#!/bin/sh\n{ for a in \"$@\"; do printf '%s\\0' \"$a\"; done; } > " + calls + ".args\n" +
		"printf 'FLOW_ROOT=%s\\0FLOW_CONNECTOR_TOKEN=%s\\0' \"$FLOW_ROOT\" \"$FLOW_CONNECTOR_TOKEN\" > " + calls + ".env\n" +
		"echo 'delivered x from alice → user [beef0001]'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(peerLocalSample, "run"), "send")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "FLOW_BIN=" + fake, "FLOW_ROOT=/homes/a",
		"FLOW_CONNECTOR_TOKEN=tok-a", "FLOW_SECRET_STORE=file"}
	cmd.Stdin = strings.NewReader(payloadFor(other))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return peerLocalRun{out: stderr.String()}, err
	}
	r := peerLocalRun{out: stdout.String(), env: map[string]string{}}
	r.args = nulFields(t, calls+".args")
	for _, kv := range nulFields(t, calls+".env") {
		k, v, _ := strings.Cut(kv, "=")
		r.env[k] = v
	}
	return r, nil
}

func TestPeerLocalSendDelivers(t *testing.T) {
	var at string
	r, err := runPeerLocal(t, func(a string) string {
		at = a
		return `{"id":"a1b2","peer":"bob","at":"` + a + `","kind":"ask","body":"ship \"v2\"? it's\nready — ok\\done   <&>","options":["Yes","No way"],"urgent":true,"reply_to_ref":"","created_at":"2026-11-09T10:00:00Z"}`
	})
	if err != nil {
		t.Fatalf("send: %v\n%s", err, r.out)
	}
	eqArgs(t, "flow argv", r.args, []string{"peer", "deliver", "--at", "/homes/a", "--ref", "a1b2",
		"--options", "Yes|No way", "--", "ship \"v2\"? it's\nready — ok\\done   <&>"})
	if r.env["FLOW_ROOT"] != at || r.env["FLOW_CONNECTOR_TOKEN"] != "tok-b" {
		t.Errorf("env = %v (want the OTHER home's root and token)", r.env)
	}
	if strings.TrimSpace(r.out) != `{"remote_ref":"beef0001"}` {
		t.Errorf("stdout = %q", r.out)
	}
}

func TestPeerLocalSendReplyAndFYI(t *testing.T) {
	r, err := runPeerLocal(t, func(a string) string {
		return `{"id":"c3","peer":"bob","at":"` + a + `","kind":"reply","body":"Yes","options":[],"urgent":false,"reply_to_ref":"b-9","created_at":"x"}`
	})
	if err != nil {
		t.Fatalf("reply: %v\n%s", err, r.out)
	}
	eqArgs(t, "reply argv", r.args, []string{"peer", "deliver", "--at", "/homes/a", "--ref", "c3",
		"--reply-to-ref", "b-9", "--", "Yes"})
	r, err = runPeerLocal(t, func(a string) string {
		return `{"id":"d4","peer":"bob","at":"` + a + `","kind":"fyi","body":"-starts with dash","options":[],"urgent":false,"reply_to_ref":"","created_at":"x"}`
	})
	if err != nil {
		t.Fatalf("fyi: %v\n%s", err, r.out)
	}
	eqArgs(t, "fyi argv", r.args, []string{"peer", "deliver", "--at", "/homes/a", "--ref", "d4",
		"--kind", "fyi", "--", "-starts with dash"})
}

func TestPeerLocalRejectsBadInput(t *testing.T) {
	for name, payload := range map[string]func(string) string{
		"not json":      func(string) string { return "nope" },
		"no id":         func(a string) string { return `{"at":"` + a + `","body":"x"}` },
		"no such home":  func(string) string { return `{"id":"x","at":"/definitely/not/here","body":"x"}` },
		"unterminated":  func(a string) string { return `{"id":"x","at":"` + a + `","body":"x` },
		"injection try": func(a string) string { return `{"id":"x'; touch /tmp/pwn-peerlocal; '","at":"/nope","body":"x"}` },
	} {
		if r, err := runPeerLocal(t, payload); err == nil {
			t.Errorf("%s: accepted (%v)", name, r.args)
		}
	}
	if _, err := os.Stat("/tmp/pwn-peerlocal"); err == nil {
		os.Remove("/tmp/pwn-peerlocal")
		t.Errorf("payload injected a shell command")
	}
}
