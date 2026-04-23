package app

import (
	"strings"
	"testing"
)

func TestMaskSensitiveContentConnectionString(t *testing.T) {
	input := []byte("DB_URL=postgres://alice:hunter2@db.internal:5432/prod")
	out, masked := maskSensitiveContent(input)
	if !masked {
		t.Error("expected masking")
	}
	if strings.Contains(string(out), "hunter2") {
		t.Errorf("password not masked: %s", out)
	}
	if !strings.Contains(string(out), "postgres://") {
		t.Errorf("protocol should be preserved: %s", out)
	}
}

func TestMaskSensitiveContentAWSKey(t *testing.T) {
	input := []byte("export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE")
	out, masked := maskSensitiveContent(input)
	if !masked {
		t.Error("expected masking")
	}
	if strings.Contains(string(out), "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key not masked: %s", out)
	}
}

func TestMaskSensitiveContentGenericKeyValue(t *testing.T) {
	input := []byte("api_key=abcdef123456789012345")
	out, masked := maskSensitiveContent(input)
	if !masked {
		t.Error("expected masking")
	}
	if strings.Contains(string(out), "abcdef123456789012345") {
		t.Errorf("api_key value not masked: %s", out)
	}
	if !strings.Contains(string(out), "api_key") {
		t.Errorf("key name should be preserved: %s", out)
	}
	if !strings.Contains(string(out), "<sensitive>") {
		t.Errorf("expected <sensitive> placeholder: %s", out)
	}
}

func TestMaskSensitiveContentGitHubToken(t *testing.T) {
	input := []byte("token: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef12")
	out, masked := maskSensitiveContent(input)
	if !masked {
		t.Error("expected masking")
	}
	if strings.Contains(string(out), "ghp_") {
		t.Errorf("GitHub token not masked: %s", out)
	}
}

func TestMaskSensitiveContentPrivateKey(t *testing.T) {
	input := []byte("-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----")
	out, masked := maskSensitiveContent(input)
	if !masked {
		t.Error("expected masking")
	}
	if strings.Contains(string(out), "MIIEowIBAAKCAQEA") {
		t.Errorf("private key content not masked: %s", out)
	}
}

func TestMaskSensitiveContentSafeContent(t *testing.T) {
	// Short values and common flow field values must NOT be masked.
	cases := []string{
		"status: in-progress",
		"priority: high",
		"## Done when",
		"- Deploy to staging",
		"work_dir: /Users/alice/code",
		"token: [this is a reference to a design token]", // brackets not in value charset
	}
	for _, c := range cases {
		out, masked := maskSensitiveContent([]byte(c))
		if masked {
			t.Errorf("false positive on %q — got %q", c, out)
		}
	}
}

func TestMaskSensitiveContentShortPassword(t *testing.T) {
	// Any length value after a sensitive field name must be masked — no minimum.
	cases := []string{
		"password=secret123",
		"password=abc",
		"pwd=x1y2z3",
		"secret=val",
	}
	for _, c := range cases {
		out, masked := maskSensitiveContent([]byte(c))
		if !masked {
			t.Errorf("expected masking for %q", c)
		}
		if !strings.Contains(string(out), "<sensitive>") {
			t.Errorf("expected <sensitive> in output for %q; got %q", c, out)
		}
	}
}

func TestMaskSensitiveContentStripeKey(t *testing.T) {
	input := []byte("STRIPE_SECRET=sk_live_ABCDEFGHIJKLMNOPQRSTUVWX")
	out, masked := maskSensitiveContent(input)
	if !masked {
		t.Error("expected masking")
	}
	if strings.Contains(string(out), "sk_live_") {
		t.Errorf("Stripe key not masked: %s", out)
	}
}
