package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var labeledSessionIDRe = regexp.MustCompile(`(?i)(?:session|thread)[ _-]?id["']?\s*[:=]\s*["']?([A-Za-z0-9._:-]{3,256})`)
var uuidSessionIDRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func ParseSessionID(out []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(line, &v); err == nil {
			if id := findSessionID(v); id != "" {
				return id, nil
			}
		}
		if id := parseSessionIDFromText(string(line)); id != "" {
			return id, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read codex exec output: %w", err)
	}
	if id := parseSessionIDFromText(string(out)); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("codex exec did not print a session id")
}

func findSessionID(v any) string {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"session_id", "sessionId", "thread_id", "threadId"} {
			if s, ok := x[key].(string); ok && sessionIDRe.MatchString(s) {
				return s
			}
		}
		if typ, _ := x["type"].(string); typ == "session_meta" {
			if payload, ok := x["payload"].(map[string]any); ok {
				if s, ok := payload["id"].(string); ok && sessionIDRe.MatchString(s) {
					return s
				}
			}
		}
		if _, hasCWD := x["cwd"]; hasCWD {
			if s, ok := x["id"].(string); ok && sessionIDRe.MatchString(s) {
				return s
			}
		}
		for _, child := range x {
			if id := findSessionID(child); id != "" {
				return id
			}
		}
	case []any:
		for _, child := range x {
			if id := findSessionID(child); id != "" {
				return id
			}
		}
	}
	return ""
}

func parseSessionIDFromText(s string) string {
	if m := labeledSessionIDRe.FindStringSubmatch(s); len(m) == 2 && sessionIDRe.MatchString(m[1]) {
		return strings.Trim(m[1], `"'`)
	}
	if m := uuidSessionIDRe.FindString(s); m != "" {
		return m
	}
	if fields := strings.Fields(strings.TrimSpace(s)); len(fields) == 1 && sessionIDRe.MatchString(fields[0]) {
		return strings.Trim(fields[0], `"'`)
	}
	return ""
}
