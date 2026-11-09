package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mdLink matches markdown links and images: [text](target) / ![alt](target).
var mdLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// htmlRef matches src="..." / href="..." / srcset="..." in inline HTML.
var htmlRef = regexp.MustCompile(`(?:src|href|srcset)="([^"]+)"`)

// relativeTargets returns the repo-relative link targets in a markdown
// file (external URLs, mailto and pure #anchors are skipped).
func relativeTargets(body string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{mdLink, htmlRef} {
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
				continue
			}
			if i := strings.Index(target, "#"); i >= 0 {
				target = target[:i]
			}
			out = append(out, target)
		}
	}
	return out
}

// TestReadmeAndDocsLinksResolve keeps the launch README and the docs/
// pages it points to free of broken relative links.
func TestReadmeAndDocsLinksResolve(t *testing.T) {
	files := []string{"../../README.md"}
	docs, err := filepath.Glob("../../docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, docs...)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range relativeTargets(string(b)) {
			p := filepath.Join(filepath.Dir(f), target)
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: broken relative link %q", f, target)
			}
		}
	}
}

// TestReadmeLaunchShape pins the approved launch structure: short, an
// install section near the top, the proof line, the "runs without you"
// section, a comparison table, and no docs for the removed bg backend.
func TestReadmeLaunchShape(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	if n := strings.Count(readme, "\n"); n >= 300 {
		t.Errorf("README is %d lines, want under 300", n)
	}
	for _, want := range []string{
		"## Install",
		"## Upgrade",
		"releases/latest/download/flow-darwin-${ARCH}",
		"xattr -d com.apple.quarantine",
		"flow init",
		"2,100+ tasks",
		"## Runs without you",
		"docs/running-without-you.md",
		"claude-mem",
		"Taskmaster",
		"Vibe Kanban",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README lacks %q", want)
		}
	}
	if strings.Contains(readme, "FLOW_TERM=bg") {
		t.Errorf("README still documents FLOW_TERM=bg")
	}
	if strings.Contains(readme, "—") {
		t.Errorf("README contains an em-dash; launch voice is no em-dashes")
	}
	// Install must come before the demo, inside the first screenful.
	install := strings.Index(readme, "## Install")
	if install < 0 || strings.Count(readme[:install], "\n") > 60 {
		t.Errorf("## Install should start within the first 60 lines")
	}
}
