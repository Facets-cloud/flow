package ghref

import (
	"net/url"
	"strconv"
	"strings"
)

func PRTagFromURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	if !strings.EqualFold(u.Host, "github.com") {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", false
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return "", false
	}
	return "gh-pr:" + strings.ToLower(parts[0]) + "/" + strings.ToLower(parts[1]) + "#" + strconv.Itoa(n), true
}
