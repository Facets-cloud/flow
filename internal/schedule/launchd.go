package schedule

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LaunchdPrefix is the reverse-DNS label prefix of every flow agent.
const LaunchdPrefix = "cloud.facets.flow."

// Launchd writes ~/Library/LaunchAgents/cloud.facets.flow.<name>.plist and
// loads it with `launchctl load -w` (macOS).
type Launchd struct {
	AgentsDir string
}

func (l *Launchd) Name() string { return "launchd" }

func (l *Launchd) path(name string) string {
	return filepath.Join(l.AgentsDir, LaunchdPrefix+name+".plist")
}

func (l *Launchd) Install(s Spec, o Options) (*Report, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	p := l.path(s.Name)
	if err := guard(p, o, "replace"); err != nil {
		return nil, err
	}
	pl := newPlan(l.Name(), o)
	if exists, _ := fileHasMarker(p); exists {
		// launchd keeps the old job until it is unloaded; a failed unload
		// (job not loaded) is fine.
		if _, err := pl.run("", true, "launchctl", "unload", p); err != nil {
			return pl.rep, err
		}
	}
	if err := pl.write(p, renderPlist(s)); err != nil {
		return pl.rep, err
	}
	if _, err := pl.run("", false, "launchctl", "load", "-w", p); err != nil {
		return pl.rep, err
	}
	return pl.rep, nil
}

func (l *Launchd) Remove(name string, o Options) (*Report, error) {
	p := l.path(name)
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("no launchd schedule %q (%s)", name, p)
	}
	if err := guard(p, o, "remove"); err != nil {
		return nil, err
	}
	pl := newPlan(l.Name(), o)
	if _, err := pl.run("", true, "launchctl", "unload", p); err != nil {
		return pl.rep, err
	}
	if err := pl.remove(p); err != nil {
		return pl.rep, err
	}
	return pl.rep, nil
}

func (l *Launchd) List() ([]Entry, error) {
	matches, err := filepath.Glob(filepath.Join(l.AgentsDir, LaunchdPrefix+"*.plist"))
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, p := range matches {
		base := filepath.Base(p)
		label := strings.TrimSuffix(base, ".plist")
		name := strings.TrimPrefix(label, LaunchdPrefix)
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		e := Entry{Name: name, Backend: l.Name(), Path: p, Source: "hand-written"}
		if strings.Contains(string(raw), Marker) {
			e.Source = "flow"
		}
		e.Command, e.When = describePlist(string(raw))
		if _, err := Runner("", "launchctl", "list", label); err == nil {
			e.Status = "loaded"
		} else {
			e.Status = "not loaded"
		}
		out = append(out, e)
	}
	return out, nil
}

func xmlEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// renderPlist produces the launchd property list for s. Calendar jobs do
// not RunAtLoad (that would fire them on every login); interval jobs do,
// so the first tick happens right after install/login.
func renderPlist(s Spec) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	fmt.Fprintf(&b, "<!-- %s: flow schedule remove %s -->\n", Marker, s.Name)
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", xmlEsc(LaunchdPrefix+s.Name))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range append([]string{s.FlowBin}, s.Args...) {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlEsc(a))
	}
	b.WriteString("\t</array>\n")
	if len(s.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range s.sortedEnvKeys() {
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlEsc(k), xmlEsc(s.Env[k]))
		}
		b.WriteString("\t</dict>\n")
	}
	if s.At != nil {
		fmt.Fprintf(&b, "\t<key>StartCalendarInterval</key>\n\t<dict>\n\t\t<key>Hour</key>\n\t\t<integer>%d</integer>\n\t\t<key>Minute</key>\n\t\t<integer>%d</integer>\n\t</dict>\n", s.At.Hour, s.At.Minute)
		b.WriteString("\t<key>RunAtLoad</key>\n\t<false/>\n")
	} else {
		fmt.Fprintf(&b, "\t<key>StartInterval</key>\n\t<integer>%d</integer>\n", int(s.Every/time.Second))
		b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	}
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", xmlEsc(s.LogPath()))
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", xmlEsc(s.ErrPath()))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

var (
	plistArgsRe     = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>(.*?)</array>`)
	plistStringRe   = regexp.MustCompile(`<string>(.*?)</string>`)
	plistIntervalRe = regexp.MustCompile(`(?s)<key>StartInterval</key>\s*<integer>(\d+)</integer>`)
	plistCalRe      = regexp.MustCompile(`(?s)<key>StartCalendarInterval</key>\s*<dict>(.*?)</dict>`)
	plistHourRe     = regexp.MustCompile(`(?s)<key>Hour</key>\s*<integer>(\d+)</integer>`)
	plistMinuteRe   = regexp.MustCompile(`(?s)<key>Minute</key>\s*<integer>(\d+)</integer>`)
)

// describePlist extracts the command line and cadence from a plist
// (flow-written or hand-written) for listing. Best-effort text scan.
func describePlist(raw string) (command, when string) {
	if m := plistArgsRe.FindStringSubmatch(raw); m != nil {
		var args []string
		for _, sm := range plistStringRe.FindAllStringSubmatch(m[1], -1) {
			args = append(args, unescapeXML(sm[1]))
		}
		command = strings.Join(args, " ")
	}
	when = "?"
	if m := plistIntervalRe.FindStringSubmatch(raw); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			when = "every " + (time.Duration(n) * time.Second).String()
		}
	} else if m := plistCalRe.FindStringSubmatch(raw); m != nil {
		h, mi := 0, 0
		if hm := plistHourRe.FindStringSubmatch(m[1]); hm != nil {
			h, _ = strconv.Atoi(hm[1])
		}
		if mm := plistMinuteRe.FindStringSubmatch(m[1]); mm != nil {
			mi, _ = strconv.Atoi(mm[1])
		}
		when = "daily " + Clock{Hour: h, Minute: mi}.String()
	}
	return command, when
}

func unescapeXML(s string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&#39;", "'", "&#34;", `"`, "&amp;", "&")
	return r.Replace(s)
}
