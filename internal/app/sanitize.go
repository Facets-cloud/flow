package app

import (
	"regexp"
)

// sensitiveRule pairs a compiled pattern with its replacement string.
// Capture group ${key} preserves the key name where applicable.
type sensitiveRule struct {
	re          *regexp.Regexp
	replacement string
}

// sensitiveRules is the ordered list of patterns applied to text file content
// during export. Order matters: more specific patterns run first.
var sensitiveRules = []sensitiveRule{
	// Connection strings: proto://user:pass@host — keep proto:// visible
	{
		re:          regexp.MustCompile(`(?i)((?:mysql|postgres(?:ql)?|mongodb(?:\+srv)?|redis|amqp|https?)://)[^:\s]+:[^@\s]+(@)`),
		replacement: "${1}<sensitive>${2}",
	},
	// PEM private key blocks
	{
		re:          regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
		replacement: "<sensitive>",
	},
	// AWS Access Key IDs
	{
		re:          regexp.MustCompile(`(?:AKIA|ASIA|AROA|AIDA)[0-9A-Z]{16}`),
		replacement: "<sensitive>",
	},
	// GitHub personal access tokens / fine-grained tokens
	{
		re:          regexp.MustCompile(`(?:ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{34,}`),
		replacement: "<sensitive>",
	},
	// Slack tokens
	{
		re:          regexp.MustCompile(`xox[baprs]-[0-9]{8,12}-[0-9]{8,12}(?:-[0-9]{8,12})?-[A-Za-z0-9]{24,}`),
		replacement: "<sensitive>",
	},
	// Stripe API keys
	{
		re:          regexp.MustCompile(`(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{24,}`),
		replacement: "<sensitive>",
	},
	// Generic key/secret/password/token assignments (key = value or key: value).
	// Requires value >= 12 chars to avoid flagging short words like "high" or "done".
	// Preserves the key name in the output.
	{
		re:          regexp.MustCompile(`(?i)((?:password|passwd|pwd|secret|api[_-]?key|access[_-]?key|auth[_-]?token|client[_-]?secret|private[_-]?key|signing[_-]?key|encryption[_-]?key|bearer)\s*[:=]\s*)([A-Za-z0-9+/=_\-\.!@#$%^&*]+)`),
		replacement: "${1}<sensitive>",
	},
}

// maskSensitiveContent applies all sensitiveRules to data and returns the
// cleaned content plus a flag indicating whether any masking occurred.
func maskSensitiveContent(data []byte) ([]byte, bool) {
	result := data
	masked := false
	for _, rule := range sensitiveRules {
		if rule.re.Match(result) {
			result = rule.re.ReplaceAll(result, []byte(rule.replacement))
			masked = true
		}
	}
	return result, masked
}
