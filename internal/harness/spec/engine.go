package spec

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"
	"time"

	"flow/internal/spawner"
)

// Vars is everything a manifest template can reference.
//
// The set is CLOSED: a manifest cannot invent a variable, because an
// unknown field is a template execution error rather than an empty
// string. That turns a typo like {{.Sesssion}} into a loud failure at
// validate time instead of a command with a hole in it.
type Vars struct {
	// SessionID is validated but still runtime data and shell-quoted.
	SessionID string

	// Author-controlled manifest metadata may remain literal.
	InjectionMarker string
	Name            string
	Binary          string

	// Data — arbitrary text, always shell-quoted (see needsQuoting).
	Prompt  string
	Inject  string
	WorkDir string
	Cwd     string
	Home    string
}

// dataVars are the Vars fields whose values are arbitrary text.
//
// An argv element whose template text mentions any of these expands to
// something the user (or a task brief) controls, so the element is
// shell-quoted as a unit. This includes {{.SessionID}}: validation gates
// identity shape, while quoting independently prevents shell interpretation.
// Everything else — literal tokens like "claude" or "--session-id" — is
// emitted bare.
//
// Adding a field to Vars means deciding which list it belongs in. Get
// that wrong in the safe direction and a command gains redundant
// quotes; get it wrong in the unsafe direction and you have a shell
// injection. When in doubt, it is a data var.
var dataVars = []string{".SessionID", ".Prompt", ".Inject", ".WorkDir", ".Cwd", ".Home"}

// funcs are the helpers a manifest may call.
var funcs = template.FuncMap{
	// encodeCwd renders an absolute path the way cwd-keyed transcript
	// layouts spell it: every "/", "." and "_" becomes "-". Claude
	// Code's ~/.claude/projects/<encoded>/ uses exactly this.
	"encodeCwd": func(cwd string) string {
		return strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(cwd)
	},
	// envOr returns $name, or the concatenation of the fallbacks when
	// it is unset — for harnesses whose data root is overridable.
	"envOr": func(name string, fallback ...string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return strings.Join(fallback, "")
	},
}

// checkTemplate parses and executes one template element against every
// supplied context. Parsing alone cannot reject a typo such as
// {{.SesssionID}}: text/template resolves struct fields only at execution.
// Callers supply both populated and empty contexts so unknown fields in
// either side of a conditional are reached during validation.
func checkTemplate(s string, contexts ...any) error {
	t, err := template.New("t").Funcs(funcs).Option("missingkey=error").Parse(s)
	if err != nil {
		return err
	}
	if err := validateTemplateFields(t, contexts); err != nil {
		return err
	}
	for _, data := range contexts {
		if err := t.Execute(io.Discard, data); err != nil {
			return err
		}
	}
	return nil
}

// validateTemplateFields walks every branch in the parse tree. Executing a
// populated and an empty context is not sufficient: nested conditionals can
// make a typo unreachable in both samples while still reachable at runtime.
func validateTemplateFields(t *template.Template, contexts []any) error {
	types := make([]reflect.Type, 0, len(contexts))
	for _, context := range contexts {
		typ := reflect.TypeOf(context)
		for typ != nil && typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ != nil {
			types = append(types, typ)
		}
	}
	for _, tmpl := range t.Templates() {
		if tmpl.Tree == nil || tmpl.Tree.Root == nil {
			continue
		}
		if err := validateTemplateNode(tmpl.Tree.Root, types); err != nil {
			return err
		}
	}
	return nil
}

func validateTemplateNode(node parse.Node, contexts []reflect.Type) error {
	if node == nil {
		return nil
	}
	value := reflect.ValueOf(node)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil
	}
	switch n := node.(type) {
	case *parse.ListNode:
		for _, child := range n.Nodes {
			if err := validateTemplateNode(child, contexts); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return validateTemplateNode(n.Pipe, contexts)
	case *parse.IfNode:
		return validateTemplateBranch(n.Pipe, n.List, n.ElseList, contexts)
	case *parse.RangeNode:
		return validateTemplateBranch(n.Pipe, n.List, n.ElseList, contexts)
	case *parse.WithNode:
		return validateTemplateBranch(n.Pipe, n.List, n.ElseList, contexts)
	case *parse.TemplateNode:
		return validateTemplateNode(n.Pipe, contexts)
	case *parse.PipeNode:
		for _, command := range n.Cmds {
			if err := validateTemplateNode(command, contexts); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			if err := validateTemplateNode(arg, contexts); err != nil {
				return err
			}
		}
	case *parse.FieldNode:
		if !templateFieldAllowed(n.Ident, contexts) {
			return fmt.Errorf("unknown template variable .%s", strings.Join(n.Ident, "."))
		}
	case *parse.ChainNode:
		if _, ok := n.Node.(*parse.DotNode); ok {
			if !templateFieldAllowed(n.Field, contexts) {
				return fmt.Errorf("unknown template variable .%s", strings.Join(n.Field, "."))
			}
			return nil
		}
		return validateTemplateNode(n.Node, contexts)
	}
	return nil
}

func validateTemplateBranch(pipe *parse.PipeNode, list, elseList *parse.ListNode, contexts []reflect.Type) error {
	for _, node := range []parse.Node{pipe, list, elseList} {
		if err := validateTemplateNode(node, contexts); err != nil {
			return err
		}
	}
	return nil
}

func templateFieldAllowed(path []string, contexts []reflect.Type) bool {
	for _, context := range contexts {
		typ := context
		valid := true
		for _, name := range path {
			for typ.Kind() == reflect.Pointer {
				typ = typ.Elem()
			}
			if typ.Kind() != reflect.Struct {
				valid = false
				break
			}
			field, ok := typ.FieldByName(name)
			if !ok {
				valid = false
				break
			}
			typ = field.Type
		}
		if valid {
			return true
		}
	}
	return false
}

func validateExpandedArgv(field string, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("%s expanded to empty argv", field)
	}
	if strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("%s expanded to a blank executable", field)
	}
	return nil
}

// expandOne renders a single template element.
//
// data is usually a Vars, but takes any struct that embeds one — the
// pointer-block template gets two extra fields on top of the standard
// environment, and embedding promotes the rest.
func expandOne(text string, data any) (string, error) {
	t, err := template.New("t").Funcs(funcs).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := t.Execute(&sb, data); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// ExpandText renders a free-form template (a managed-block body, a hook
// entry) with no quoting and no path cleaning.
func ExpandText(text string, data any) (string, error) {
	return expandOne(text, data)
}

// needsQuoting reports whether an element's TEMPLATE TEXT (not its
// expansion) references arbitrary data and therefore must be quoted.
//
// Deciding from the source text rather than the result is deliberate:
// after expansion every element is just a string and the provenance is
// gone. A prompt that happens to look like a flag must still be quoted;
// a literal flag must still not be.
func needsQuoting(text string) bool {
	for _, dv := range dataVars {
		if strings.Contains(text, dv) {
			return true
		}
	}
	return false
}

// isPurelyConditional reports whether an element's template consists
// ENTIRELY of conditional blocks, with no unconditional content.
//
// This is what separates the two meanings of "expanded to empty":
//
//	{{if .Inject}}{{.Inject}}{{end}}   → purely conditional; empty means
//	                                     the optional argument is absent,
//	                                     so the element is dropped
//	{{.Prompt}}                        → has unconditional content; empty
//	                                     means the prompt IS empty, so the
//	                                     element is kept as ''
//
// Getting this wrong is not cosmetic. Dropping an empty positional
// argument shifts every argument after it, so a harness would silently
// receive its flags in the wrong slots.
func isPurelyConditional(text string) bool {
	depth := 0
	rest := text
	for {
		open := strings.Index(rest, "{{")
		if open < 0 {
			// Trailing literal: only conditional-free content at
			// depth 0 disqualifies.
			return depth != 0 || strings.TrimSpace(rest) == ""
		}
		if depth == 0 && strings.TrimSpace(rest[:open]) != "" {
			return false
		}
		rest = rest[open+2:]
		close := strings.Index(rest, "}}")
		if close < 0 {
			// Malformed; the parser reports it properly elsewhere.
			return false
		}
		action := strings.TrimSpace(rest[:close])
		rest = rest[close+2:]
		switch {
		case strings.HasPrefix(action, "if"), strings.HasPrefix(action, "range"), strings.HasPrefix(action, "with"):
			depth++
		case action == "end":
			if depth > 0 {
				depth--
			}
		case strings.HasPrefix(action, "else"):
			// Same depth.
		default:
			// A value action outside any conditional.
			if depth == 0 {
				return false
			}
		}
	}
}

// ExpandShell renders argv as a single shell command line.
//
// Each element is expanded independently, quoted iff its template text
// referenced a data variable, and joined with spaces. Elements that
// expand to empty are DROPPED — that is how a manifest expresses an
// optional argument, e.g. an injection that is only present sometimes:
//
//	"{{if .Inject}}{{.InjectionMarker}}\n{{.Inject}}{{end}}"
//
// prelude, when non-empty, is prepended with " && " for harnesses that
// need shell setup (raised file limits) before the binary runs. It is
// author-controlled and emitted verbatim.
func ExpandShell(argv []string, prelude string, v Vars) (string, error) {
	parts := make([]string, 0, len(argv))
	rawParts := make([]string, 0, len(argv))
	for i, elem := range argv {
		out, err := expandOne(elem, v)
		if err != nil {
			return "", fmt.Errorf("argv[%d]: %w", i, err)
		}
		if out == "" && isPurelyConditional(elem) {
			continue
		}
		rawParts = append(rawParts, out)
		if needsQuoting(elem) {
			out = spawner.ShellQuote(out)
		}
		parts = append(parts, out)
	}
	if err := validateExpandedArgv("command", rawParts); err != nil {
		return "", err
	}
	cmd := strings.Join(parts, " ")
	if prelude != "" {
		cmd = prelude + " && " + cmd
	}
	return cmd, nil
}

// ExpandArgv renders argv for exec.Command.
//
// No quoting happens here and none is wanted: exec passes each element
// to the kernel as a separate argument, so a prompt containing quotes,
// newlines or $(...) is inert. Empty elements are dropped on the same
// rule as ExpandShell.
func ExpandArgv(argv []string, v Vars) ([]string, error) {
	out := make([]string, 0, len(argv))
	for i, elem := range argv {
		s, err := expandOne(elem, v)
		if err != nil {
			return nil, fmt.Errorf("argv[%d]: %w", i, err)
		}
		if s == "" && isPurelyConditional(elem) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// ExpandPath renders a filesystem path template and cleans the result.
// No quoting — the value is used with os.Open, not a shell.
func ExpandPath(tmpl string, v Vars) (string, error) {
	s, err := expandOne(tmpl, v)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("path template expanded to empty")
	}
	return filepath.Clean(s), nil
}

// ResolvePath renders a path template and, if the result contains a
// glob metacharacter, resolves it against the filesystem.
//
// Globbing exists because transcript layouts are not always derivable
// from a session id. Codex writes
// ~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<id>.jsonl and omp
// writes <cwd-slug>/<timestamp>_<id>.jsonl — both embed a creation time
// flow does not know. A pattern like
//
//	{{.Home}}/.codex/sessions/*/*/*/rollout-*-{{.SessionID}}.jsonl
//
// keeps those expressible without inventing a date-arithmetic DSL.
//
// The NEWEST match wins. Ambiguity is possible in principle (a session
// id appearing under two dates) and picking the most recent is both the
// useful answer and a stable one.
func ResolvePath(tmpl string, v Vars) (string, error) {
	path, err := ExpandPath(tmpl, v)
	if err != nil {
		return "", err
	}
	if !strings.ContainsAny(path, "*?[") {
		return path, nil
	}
	matches, err := filepath.Glob(path)
	if err != nil {
		return "", fmt.Errorf("bad path pattern %q: %w", path, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no file matches %s", path)
	}
	newest, newestMod := matches[0], time.Time{}
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.ModTime().After(newestMod) {
			newest, newestMod = m, info.ModTime()
		}
	}
	return newest, nil
}

// withInjection folds an injection into a prompt the way every harness
// flow has met does it: two blank-line-separated blocks, the marker
// announcing that what follows came from `flow do --with`.
func withInjection(prompt, marker, inject string) string {
	if inject == "" {
		return prompt
	}
	return prompt + "\n\n" + marker + "\n" + inject
}
