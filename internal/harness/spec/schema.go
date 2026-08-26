// Package spec turns a declarative harness manifest into a working
// harness.Harness, so integrating a coding agent is a file rather than
// a release.
//
// A manifest is TOML. Every capability is an OPTIONAL table: omit
// [resume] and the resulting harness reports that it cannot resume,
// omit [transcript] and it reports that it keeps no readable
// conversation. That maps one-to-one onto harness.Harness's nil-when-
// unsupported accessors, so "the table is absent" and "the capability
// is nil" are the same fact expressed twice.
//
// # Three template contexts
//
// Manifests interpolate values with text/template, but the rules differ
// by where the result is going:
//
//   - SHELL templates ([launch].argv, [resume].argv) are expanded per
//     element, selectively shell-quoted, and joined with spaces. The
//     result is handed to a terminal.
//   - ARGV templates ([headless], [liveness].argv) are expanded per
//     element and passed to exec.Command untouched — no shell, so no
//     quoting.
//   - PATH templates ([transcript].path, [skills].dir) are expanded and
//     cleaned as filesystem paths.
//
// Mixing them up is the classic way to create a quoting bug, so the
// engine keeps them as three distinct functions rather than one
// "expand" with a flag.
package spec

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"flow/internal/harness"
	"flow/internal/harness/managedblock"
)

// SchemaVersion is the only manifest schema this binary understands.
// A manifest declaring anything else is rejected by name rather than
// silently half-read.
const SchemaVersion = 1

// Spec is a decoded harness manifest.
//
// Optional capability tables are POINTERS: nil means "this harness does
// not have that capability", which is exactly what the corresponding
// harness.Harness accessor will report.
type Spec struct {
	Schema int `toml:"schema"`

	Name   string `toml:"name"`
	Binary string `toml:"binary"`

	// SessionEnv is the env var the harness exports inside a session,
	// which flow reads to answer "which task is THIS session bound to".
	//
	// OPTIONAL, because a real and common category of agent exports
	// nothing: codex and omp both mint session ids internally and never
	// publish them. Such a harness still launches, resumes, installs
	// skills and wires hooks — it just cannot support `flow do --here`
	// or the hook reverse-lookup, and flow says so rather than
	// pretending.
	SessionEnv string `toml:"session_env"`

	Session  SessionSpec  `toml:"session"`
	Launch   LaunchSpec   `toml:"launch"`
	Liveness LivenessSpec `toml:"liveness"`
	Vocab    VocabSpec    `toml:"vocab"`

	Resume     *ResumeSpec     `toml:"resume"`
	Headless   *HeadlessSpec   `toml:"headless"`
	Transcript *TranscriptSpec `toml:"transcript"`
	Skills     *SkillsSpec     `toml:"skills"`
	Hooks      *HooksSpec      `toml:"hooks"`

	// Source is the file this spec was read from, used to make
	// validation errors point at something the user can edit. Not a
	// manifest key.
	Source string `toml:"-"`
}

// SessionSpec describes how flow obtains and checks a session id.
type SessionSpec struct {
	// Strategy is how a new id is minted:
	//
	//   uuid4        generate locally (claude)
	//   uuid7        generate locally, time-ordered (praxis)
	//   exec-capture run Argv and pull the id out of stdout with
	//                Capture — for harnesses that mint their own
	//
	Strategy string `toml:"strategy"`

	// Argv and Capture are required by, and only meaningful for, the
	// exec-capture strategy. Capture must contain exactly one
	// capturing group: the session id.
	Argv    []string `toml:"argv"`
	Capture string   `toml:"capture"`

	// Validate is a REQUIRED regexp that a string must match to be a
	// legal session id for this harness.
	//
	// It is not merely a nicety. Shell templates interpolate
	// {{.SessionID}} WITHOUT quoting (see quoting rules in engine.go),
	// which is only safe because this pattern gates every id first. A
	// manifest without it would smuggle arbitrary text into a shell
	// command, so loading one is an error.
	Validate string `toml:"validate"`

	// VerifyCwd asks flow to confirm, when binding an existing
	// session, that the harness's transcript really sits at the path
	// implied by (work_dir, session id). Only meaningful for
	// cwd-keyed transcript layouts; harnesses that key transcripts by
	// id alone leave it false and the check no-ops.
	VerifyCwd bool `toml:"verify_cwd"`
}

// LaunchSpec builds the interactive first-run command.
type LaunchSpec struct {
	// Argv is a SHELL template: expanded per element, selectively
	// quoted, joined with spaces.
	Argv []string `toml:"argv"`

	// PermissionFlag is appended verbatim when the caller asks to
	// skip per-tool approvals. Empty means the harness has no such
	// flag and the request is silently a no-op.
	PermissionFlag []string `toml:"permission_flag"`

	// Prelude is a shell fragment prepended to the command (joined
	// with " && "), for harnesses that need process limits raised
	// before launch. Author-controlled, never quoted.
	Prelude string `toml:"prelude"`
}

// ResumeSpec builds the command that continues an existing session.
type ResumeSpec struct {
	Argv           []string `toml:"argv"`
	PermissionFlag []string `toml:"permission_flag"`
	Prelude        string   `toml:"prelude"`
}

// HeadlessSpec describes non-interactive execution.
type HeadlessSpec struct {
	// RunArgv is the sessionless one-shot used by `flow done`'s
	// close-out sweep. Output is discarded; only the exit code matters.
	RunArgv []string `toml:"run_argv"`

	// AutoArgv is the session-pinned autonomous run (`flow do --auto`).
	// Unlike RunArgv it keeps a transcript, so the run's own close-out
	// and `flow transcript` have something to read.
	AutoArgv []string `toml:"auto_argv"`
}

// LivenessSpec describes how flow counts running sessions.
type LivenessSpec struct {
	// Probe selects the mechanism:
	//
	//   ps    scan the process table for Binary, extract ids with Match
	//   exec  run Argv and extract ids with Match from its output
	//   none  the harness cannot be probed; always reports nothing
	//
	Probe string `toml:"probe"`

	// Match is a regexp with exactly one capturing group yielding a
	// session id. Required unless Probe is "none".
	Match string `toml:"match"`

	Argv []string `toml:"argv"`
}

// TranscriptSpec locates and decodes a session's conversation.
type TranscriptSpec struct {
	// Path is a PATH template for the transcript file. Two shapes are
	// common and both are expressible: cwd-keyed layouts reference
	// {{.Cwd}} (usually via the encodeCwd function), id-keyed layouts
	// reference only {{.SessionID}}.
	Path string `toml:"path"`

	// Format is the on-disk encoding. Only "jsonl" is understood.
	Format string `toml:"format"`

	Map TranscriptMap `toml:"map"`
}

// TranscriptMap names the fields flow needs inside each record.
//
// Values are dotted paths into the decoded JSON. A "[]" suffix on a
// segment iterates an array, so "message.content[].text" yields the
// text of every content block. A path that does not resolve yields
// nothing rather than an error — records legitimately differ in shape
// (a tool-call block has no .text), and skipping is the correct
// response to a block that simply is not the kind being asked for.
type TranscriptMap struct {
	// Role selects user/assistant so the renderer can label a turn.
	Role string `toml:"role"`

	// Text is the conversation body. Required.
	Text string `toml:"text"`

	// ToolBlock is the VALUE of a content block's "type" field that
	// marks it as a tool call (claude: "tool_use"; praxis:
	// "toolCall"). Blocks are discriminated on "type" because both
	// formats flow has met spell it that way.
	ToolBlock string `toml:"tool_block"`

	// ToolName is the path to the tool's name within such a block.
	ToolName string `toml:"tool_name"`
}

// SkillsSpec says where flow's skill tree goes and how the harness
// finds it.
type SkillsSpec struct {
	// Discovery is how the harness locates the skill:
	//
	//   native   the harness scans Dir on its own (claude)
	//   pointer  the harness scans nothing; flow writes the tree AND a
	//            managed block in an instructions file pointing at it
	//
	// "pointer" is what makes skills work for the majority of agents,
	// which have no skill mechanism but do read an ambient AGENTS.md.
	Discovery string `toml:"discovery"`

	// Dir is a PATH template for the skill directory. SKILL.md and
	// VERSION live directly inside it.
	Dir string `toml:"dir"`

	// OwnsDir declares whether this harness's manifest may DELETE Dir
	// on uninstall.
	//
	// False is the important case: praxis natively scans
	// ~/.claude/skills, so its manifest points at a directory claude
	// owns. Uninstalling praxis must not take claude's skill with it.
	// Defaults to true — a harness pointing at its own private
	// directory is the common case.
	OwnsDir *bool `toml:"owns_dir"`

	// RequireFrontmatter lists YAML frontmatter keys the harness
	// demands in SKILL.md. Checked BEFORE writing, so a harness that
	// rejects a skill missing `description:` (praxis does, outright)
	// produces a clear error instead of a silently ignored install.
	RequireFrontmatter []string `toml:"require_frontmatter"`

	Pointer *PointerSpec `toml:"pointer"`
}

// Owns reports whether uninstall may delete the skill directory.
func (s *SkillsSpec) Owns() bool { return s.OwnsDir == nil || *s.OwnsDir }

// PointerSpec describes the managed block that points a
// no-skill-mechanism harness at flow's skill tree.
type PointerSpec struct {
	// File is a PATH template for the instructions file the harness
	// reads automatically.
	File string `toml:"file"`

	// Comment selects the marker syntax: html, hash or slash.
	Comment string `toml:"comment"`

	// Block is the body written between the markers. Beyond the usual
	// variables it may use:
	//
	//   {{.SkillPath}}      absolute path to the installed SKILL.md
	//   {{.HookDirective}}  the self-invocation instructions, present
	//                       only when the instruction-directive hook
	//                       strategy is enabled; empty otherwise
	//
	Block string `toml:"block"`
}

// HooksSpec describes how flow gets its SessionStart and
// UserPromptSubmit context in front of the agent.
type HooksSpec struct {
	// Strategies is an ordered list; `flow skill install` performs
	// every one that is listed:
	//
	//   config-patch           register flow's commands in the
	//                          harness's own hook config. Deterministic,
	//                          and the only one that covers sessions the
	//                          user starts outside flow.
	//   prompt-prelude         flow prepends the SessionStart context to
	//                          the launch prompt it already builds.
	//                          Deterministic, but only for sessions flow
	//                          spawns. Nothing to install.
	//   instruction-directive  the skills managed block tells the agent
	//                          to run the hook commands itself. Covers
	//                          ad-hoc sessions and drift, but is
	//                          best-effort: it depends on the agent
	//                          complying.
	//
	// A harness with no hook system reaches near-parity by combining
	// the last two.
	Strategies []string `toml:"strategies"`

	ConfigPatch *ConfigPatchSpec `toml:"config_patch"`
}

// Has reports whether a strategy is enabled.
func (h *HooksSpec) Has(strategy string) bool {
	if h == nil {
		return false
	}
	for _, s := range h.Strategies {
		if s == strategy {
			return true
		}
	}
	return false
}

// ConfigPatchSpec locates the harness's hook config and describes the
// entry to add. JSON only — both harnesses flow has met use it.
type ConfigPatchSpec struct {
	// File is a PATH template for the config file. Created if absent.
	File string `toml:"file"`

	// Events maps flow's event name (SessionStart, UserPromptSubmit)
	// to where and what to write. A harness that supports only one of
	// them simply omits the other.
	Events map[string]EventPatchSpec `toml:"events"`
}

// EventPatchSpec is one event's insertion point and payload.
type EventPatchSpec struct {
	// Pointer is a slash-separated path to the ARRAY of hook entries,
	// e.g. "/hooks/SessionStart". Missing objects along the way are
	// created.
	Pointer string `toml:"pointer"`

	// Entry is a JSON template for the element to append. It may use
	// {{.Command}} (the flow command being registered) and is rendered
	// then parsed, so a malformed result is caught before the file is
	// touched.
	//
	// Shapes differ wildly — claude wants
	// {"matcher":…,"hooks":[{"type":"command","command":…}]} while
	// praxis accepts a bare "command" string — which is exactly why
	// this is a template rather than a struct.
	Entry string `toml:"entry"`
}

// VocabSpec is the harness's agent-facing dialect.
type VocabSpec struct {
	Product     string `toml:"product"`
	ContextFile string `toml:"context_file"`
	AskTool     string `toml:"ask_tool"`
	SkillHint   string `toml:"skill_hint"`
}

// Recognized enum values, kept next to the validator that enforces them.
var (
	sessionStrategies = map[string]bool{"uuid4": true, "uuid7": true, "exec-capture": true}
	livenessProbes    = map[string]bool{"ps": true, "exec": true, "none": true}
	transcriptFormats = map[string]bool{"jsonl": true}
	skillDiscovery    = map[string]bool{"native": true, "pointer": true}
	hookStrategies    = map[string]bool{
		StrategyConfigPatch:          true,
		StrategyPromptPrelude:        true,
		StrategyInstructionDirective: true,
	}
	// hookEvents are the two lifecycle points flow needs. A manifest
	// naming anything else is a typo, not a feature request.
	hookEvents = map[string]bool{EventSessionStart: true, EventUserPromptSubmit: true}
)

// Hook strategy names. Aliased from the harness package so the
// manifest vocabulary and the interface contract cannot drift apart.
const (
	StrategyConfigPatch          = harness.StrategyConfigPatch
	StrategyPromptPrelude        = harness.StrategyPromptPrelude
	StrategyInstructionDirective = harness.StrategyInstructionDirective
)

// Hook event names, matching harness.HookWirer's two install methods.
const (
	EventSessionStart     = "SessionStart"
	EventUserPromptSubmit = "UserPromptSubmit"
)

// Decode parses a manifest from TOML bytes.
//
// Unknown keys are an ERROR, not a warning. A typo like `sesion_env`
// would otherwise leave the field at its zero value and produce a
// harness that misbehaves at spawn time, hours later, with no clue
// pointing back at the manifest.
func Decode(data []byte, source string) (*Spec, error) {
	var s Spec
	md, err := toml.Decode(string(data), &s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("%s: unknown key(s): %s", source, strings.Join(keys, ", "))
	}
	s.Source = source
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Validate rejects a manifest that would produce a broken harness.
//
// Everything checkable without running anything is checked here, so
// `flow harness validate` can tell the user what is wrong before they
// discover it mid-spawn. Regexps are compiled (not just non-empty) and
// capture-group counts are enforced, because "one group" is a real
// contract the extraction code depends on.
func (s *Spec) Validate() error {
	var errs []string
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if s.Schema != SchemaVersion {
		bad("schema = %d, but this flow binary understands schema %d", s.Schema, SchemaVersion)
	}
	if s.Name == "" {
		bad("name is required")
	}
	if s.Binary == "" {
		bad("binary is required")
	}
	// session_env is deliberately NOT required — see SessionEnv.

	// --- session -----------------------------------------------------
	if !sessionStrategies[s.Session.Strategy] {
		bad("session.strategy = %q; want one of uuid4, uuid7, exec-capture", s.Session.Strategy)
	}
	if s.Session.Strategy == "exec-capture" {
		if len(s.Session.Argv) == 0 {
			bad("session.argv is required for strategy = exec-capture")
		}
		checkTemplates(bad, "session.argv", s.Session.Argv)
		checkOneGroup(bad, "session.capture", s.Session.Capture, true)
	}
	if s.Session.Validate == "" {
		bad("session.validate is required so captured and ambient session ids can be rejected before use")
	} else if _, err := regexp.Compile(s.Session.Validate); err != nil {
		bad("session.validate is not a valid regexp: %v", err)
	}
	if s.Session.VerifyCwd && s.Transcript == nil {
		bad("session.verify_cwd = true needs a [transcript] table to know where to look")
	}

	// --- launch / resume ---------------------------------------------
	if len(s.Launch.Argv) == 0 {
		bad("launch.argv is required")
	}
	checkTemplates(bad, "launch.argv", s.Launch.Argv)
	checkTemplates(bad, "launch.permission_flag", s.Launch.PermissionFlag)
	checkTemplates(bad, "launch.prelude", []string{s.Launch.Prelude})
	if s.Resume != nil {
		if len(s.Resume.Argv) == 0 {
			bad("resume.argv is required when [resume] is present (omit the table if the harness cannot resume)")
		}
		checkTemplates(bad, "resume.argv", s.Resume.Argv)
		checkTemplates(bad, "resume.permission_flag", s.Resume.PermissionFlag)
		checkTemplates(bad, "resume.prelude", []string{s.Resume.Prelude})
	}

	// --- headless ----------------------------------------------------
	if s.Headless != nil {
		if len(s.Headless.RunArgv) == 0 && len(s.Headless.AutoArgv) == 0 {
			bad("[headless] needs run_argv, auto_argv, or both")
		}
		checkTemplates(bad, "headless.run_argv", s.Headless.RunArgv)
		checkTemplates(bad, "headless.auto_argv", s.Headless.AutoArgv)
	}

	// --- liveness ----------------------------------------------------
	if !livenessProbes[s.Liveness.Probe] {
		bad("liveness.probe = %q; want one of ps, exec, none", s.Liveness.Probe)
	}
	if s.Liveness.Probe != "none" {
		checkOneGroup(bad, "liveness.match", s.Liveness.Match, true)
	}
	if s.Liveness.Probe == "exec" && len(s.Liveness.Argv) == 0 {
		bad("liveness.argv is required for probe = exec")
	}
	checkTemplates(bad, "liveness.argv", s.Liveness.Argv)

	// --- transcript --------------------------------------------------
	if s.Transcript != nil {
		if s.Transcript.Path == "" {
			bad("transcript.path is required")
		}
		checkTemplates(bad, "transcript.path", []string{s.Transcript.Path})
		if !transcriptFormats[s.Transcript.Format] {
			bad("transcript.format = %q; only jsonl is understood", s.Transcript.Format)
		}
		if s.Transcript.Map.Text == "" {
			bad("transcript.map.text is required to render a transcript")
		}
	}

	// --- skills ------------------------------------------------------
	if s.Skills != nil {
		if !skillDiscovery[s.Skills.Discovery] {
			bad("skills.discovery = %q; want native or pointer", s.Skills.Discovery)
		}
		if s.Skills.Dir == "" {
			bad("skills.dir is required")
		}
		checkTemplates(bad, "skills.dir", []string{s.Skills.Dir})
		if s.Skills.Discovery == "pointer" {
			if s.Skills.Pointer == nil {
				bad("skills.discovery = pointer needs a [skills.pointer] table saying which file to write the block into")
			} else {
				p := s.Skills.Pointer
				if p.File == "" {
					bad("skills.pointer.file is required")
				}
				if p.Block == "" {
					bad("skills.pointer.block is required (the text the harness will read)")
				}
				if !managedblock.Valid(managedblock.Comment(p.Comment)) {
					bad("skills.pointer.comment = %q; want html, hash or slash", p.Comment)
				}
				checkTemplates(bad, "skills.pointer.file", []string{p.File})
				checkTemplates(bad, "skills.pointer.block", []string{p.Block}, validationBlockContexts()...)
			}
		}
	}

	// --- hooks -------------------------------------------------------
	if s.Hooks != nil {
		if len(s.Hooks.Strategies) == 0 {
			bad("[hooks] needs at least one entry in strategies (omit the table if the harness has no way to receive flow's context)")
		}
		for _, st := range s.Hooks.Strategies {
			if !hookStrategies[st] {
				bad("hooks.strategies contains %q; want %s, %s or %s",
					st, StrategyConfigPatch, StrategyPromptPrelude, StrategyInstructionDirective)
			}
		}
		if s.Hooks.Has(StrategyConfigPatch) {
			cp := s.Hooks.ConfigPatch
			switch {
			case cp == nil:
				bad("hooks.strategies includes %s but there is no [hooks.config_patch] table", StrategyConfigPatch)
			default:
				if cp.File == "" {
					bad("hooks.config_patch.file is required")
				}
				checkTemplates(bad, "hooks.config_patch.file", []string{cp.File})
				if len(cp.Events) == 0 {
					bad("hooks.config_patch declares no events; add [hooks.config_patch.events.%s]", EventSessionStart)
				}
				for name, ev := range cp.Events {
					if !hookEvents[name] {
						bad("hooks.config_patch.events.%s: unknown event; want %s or %s",
							name, EventSessionStart, EventUserPromptSubmit)
					}
					if ev.Pointer == "" {
						bad("hooks.config_patch.events.%s.pointer is required", name)
					}
					if ev.Entry == "" {
						bad("hooks.config_patch.events.%s.entry is required", name)
					}
					checkTemplates(bad, "hooks.config_patch.events."+name+".entry", []string{ev.Entry}, validationEntryContexts()...)
				}
			}
		}
		// The instruction-directive strategy has nowhere to put its text
		// unless a pointer block exists to carry it.
		if s.Hooks.Has(StrategyInstructionDirective) {
			if s.Skills == nil || s.Skills.Discovery != "pointer" {
				bad("hooks.strategies includes %s, which is delivered inside the skills pointer block — set skills.discovery = \"pointer\"",
					StrategyInstructionDirective)
			}
		}
		// prompt-prelude injects into the launch prompt, which only
		// exists if the harness can be launched with one.
		if s.Hooks.Has(StrategyPromptPrelude) && len(s.Launch.Argv) == 0 {
			bad("hooks.strategies includes %s but there is no launch.argv to prepend to", StrategyPromptPrelude)
		}
	}

	// --- vocab -------------------------------------------------------
	if s.Vocab.Product == "" {
		bad("vocab.product is required (the harness's human-facing name)")
	}

	if len(errs) == 0 {
		return nil
	}
	src := s.Source
	if src == "" {
		src = "harness manifest"
	}
	return fmt.Errorf("%s is not a usable harness manifest:\n  - %s", src, strings.Join(errs, "\n  - "))
}

// checkOneGroup enforces "compiles, and has exactly one capturing
// group" — the contract every id-extraction regexp in a manifest must
// meet, since the engine reads submatch[1] unconditionally.
func checkOneGroup(bad func(string, ...any), field, pattern string, required bool) {
	if pattern == "" {
		if required {
			bad("%s is required", field)
		}
		return
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		bad("%s is not a valid regexp: %v", field, err)
		return
	}
	if n := re.NumSubexp(); n != 1 {
		bad("%s must have exactly 1 capturing group (the session id), found %d", field, n)
	}
}

// checkTemplates parses and executes each element so malformed actions and
// unknown variables are reported against their manifest key at load time.
func checkTemplates(bad func(string, ...any), field string, elems []string, contexts ...any) {
	if len(contexts) == 0 {
		contexts = validationVarsContexts()
	}
	for i, e := range elems {
		if err := checkTemplate(e, contexts...); err != nil {
			bad("%s[%d]: %v", field, i, err)
		}
	}
}

func populatedValidationVars() Vars {
	return Vars{
		SessionID:       "session-id",
		InjectionMarker: "injection-marker",
		Name:            "harness",
		Binary:          "binary",
		Prompt:          "prompt",
		Inject:          "inject",
		WorkDir:         "/workdir",
		Cwd:             "/cwd",
		Home:            "/home",
	}
}

func validationVarsContexts() []any {
	return []any{populatedValidationVars(), Vars{}}
}

func validationBlockContexts() []any {
	return []any{
		blockVars{Vars: populatedValidationVars(), SkillPath: "/skills/flow/SKILL.md", HookDirective: "hook directive"},
		blockVars{},
	}
}

func validationEntryContexts() []any {
	return []any{
		entryVars{Vars: populatedValidationVars(), Command: "flow hook session-start", Event: EventSessionStart},
		entryVars{},
	}
}
