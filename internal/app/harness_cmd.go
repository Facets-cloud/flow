package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flow/internal/harness"
	"flow/internal/harness/registry"
	"flow/internal/harness/spec"
)

// cmdHarness is the user's window into harness resolution: which
// harnesses exist, what a manifest actually parsed to, and why one was
// rejected. Without it, debugging a manifest means reading Go.
func cmdHarness(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: flow harness list|show <name>|validate <file>")
		return 2
	}
	switch args[0] {
	case "list":
		return harnessList()
	case "show":
		return harnessShow(args[1:])
	case "validate":
		return harnessValidate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\nusage: flow harness list|show <name>|validate <file>\n", args[0])
		return 2
	}
}

// capabilityList renders what a harness can do, so "why did flow refuse
// to resume" has a one-command answer.
func capabilityList(h harness.Harness) string {
	caps := []struct {
		name string
		on   bool
	}{
		{"resume", h.Resume() != nil},
		{"headless", h.Headless() != nil},
		{"transcript", h.Transcript() != nil},
		{"skills", h.Skills() != nil},
		{"hooks", h.Hooks() != nil},
		{"background", h.Background() != nil},
	}
	var on []string
	for _, c := range caps {
		if c.on {
			on = append(on, c.name)
		}
	}
	if len(on) == 0 {
		return "(none)"
	}
	return strings.Join(on, " ")
}

// harnessOrigin distinguishes a manifest-driven harness from a
// compiled-in one, and names the file for the former.
func harnessOrigin(h harness.Harness) string {
	if a, ok := h.(*spec.Adapter); ok {
		return a.Spec().Source
	}
	return "built-in"
}

func harnessList() int {
	all := allHarnesses()
	rows := make([][3]string, 0, len(all))
	for _, h := range all {
		rows = append(rows, [3]string{string(h.Name()), harnessOrigin(h), capabilityList(h)})
	}

	nameW, srcW := len("HARNESS"), len("SOURCE")
	for _, r := range rows {
		if len(r[0]) > nameW {
			nameW = len(r[0])
		}
		if len(r[1]) > srcW {
			srcW = len(r[1])
		}
	}
	fmt.Printf("%-*s  %-*s  %s\n", nameW, "HARNESS", srcW, "SOURCE", "CAPABILITIES")
	for _, r := range rows {
		fmt.Printf("%-*s  %-*s  %s\n", nameW, r[0], srcW, r[1], r[2])
	}

	// Load problems are reported here rather than on every command,
	// so a broken experimental manifest doesn't spam `flow list` — but
	// is never invisible either.
	if errs := registry.Errors(); len(errs) > 0 {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "%d manifest problem(s):\n", len(errs))
		for _, err := range errs {
			fmt.Fprintf(os.Stderr, "  - %v\n", err)
		}
	}
	if root, err := flowRoot(); err == nil {
		fmt.Printf("\nmanifests: %s\n", filepath.Join(root, registry.ManifestDir))
	}
	return 0
}

func harnessShow(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: flow harness show <name>")
		return 2
	}
	h, err := harnessByName(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	v := h.Vocab()
	fmt.Printf("name:         %s\n", h.Name())
	fmt.Printf("source:       %s\n", harnessOrigin(h))
	fmt.Printf("binary:       %s\n", h.Binary())
	if env := h.SessionIDEnvVar(); env != "" {
		fmt.Printf("session env:  %s\n", env)
	} else {
		fmt.Printf("session env:  (none — publishes no session id, so `flow do --here` and hook binding are unavailable)\n")
	}
	fmt.Printf("capabilities: %s\n", capabilityList(h))
	if hk := h.Hooks(); hk != nil {
		fmt.Printf("hooks via:    %s\n", strings.Join(hk.Strategies(), ", "))
	}
	if sk := h.Skills(); sk != nil {
		owned := "owns the directory"
		if !sk.OwnsSkillDir() {
			owned = "shares another harness's directory (uninstall leaves it)"
		}
		if path, err := sk.SkillInstallPath(); err == nil {
			fmt.Printf("skill:        %s — %s\n", path, owned)
		}
	}
	fmt.Printf("\nvocabulary\n")
	fmt.Printf("  product:      %s\n", v.Product)
	fmt.Printf("  context file: %s\n", v.ContextFile)
	fmt.Printf("  ask tool:     %s\n", orNone(v.AskTool))
	fmt.Printf("  skill hint:   %s\n", orNone(v.SkillHint))

	// A rendered sample is worth more than echoing the templates back:
	// it shows the quoting the harness will actually receive.
	const sample = "658bf2be-5ae3-4842-a8a4-e0d0b785514d"
	sampleOpts := harness.LaunchOpts{WorkDir: "/path/to/repo"}
	if h.ValidateSessionID(sample) == nil {
		fmt.Printf("\nsample commands (session id %s, work_dir %s)\n", sample, sampleOpts.WorkDir)
		fmt.Printf("  launch: %s\n", h.LaunchCmd(sample, "do the thing", sampleOpts))
		if r := h.Resume(); r != nil {
			fmt.Printf("  resume: %s\n", r.ResumeCmd(sample, sampleOpts))
		}
	}
	return 0
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// harnessValidate checks a manifest file without installing it, so a
// user can iterate before dropping it into the manifest directory.
func harnessValidate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: flow harness validate <file.toml>")
		return 2
	}
	path := args[0]
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	s, err := spec.Decode(data, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	a, err := spec.New(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	fmt.Printf("%s: valid manifest for harness %q\n", path, s.Name)
	fmt.Printf("  capabilities: %s\n", capabilityList(a))
	if existing, err := harnessByName(s.Name); err == nil && harnessOrigin(existing) != path {
		fmt.Printf("  note: installing this would shadow the existing %q harness (%s)\n",
			s.Name, harnessOrigin(existing))
	}
	return 0
}
