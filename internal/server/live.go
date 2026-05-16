package server

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var psRunner = func() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,command").Output()
}

var claudeSessionArgRe = regexp.MustCompile(
	`(?:--session-id|--resume)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

func liveClaudeSessions() (map[string]bool, error) {
	live, err := liveAgentSessions()
	if err != nil {
		return nil, err
	}
	return live, nil
}

func liveAgentSessions() (map[string]bool, error) {
	out, err := psRunner()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	live := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "claude") {
			matches := claudeSessionArgRe.FindAllStringSubmatch(line, -1)
			for _, match := range matches {
				if len(match) >= 2 {
					live[strings.ToLower(match[1])] = true
				}
			}
		}
		if strings.Contains(lower, "codex") && strings.Contains(lower, "resume") {
			for _, id := range anySessionUUIDs(line) {
				live[strings.ToLower(id)] = true
			}
		}
	}
	return live, nil
}

func anySessionUUIDs(line string) []string {
	re := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	return re.FindAllString(line, -1)
}
