// Package registry owns which harness adapters this flow binary knows
// about, and how a stored name, the ambient environment, or a bare
// default resolves to one.
//
// It exists so that exactly one package imports concrete adapters.
// internal/app asks the registry questions and never mentions claude
// (or codex, or praxis) by name — which is what makes adding a harness
// a change here rather than a change spread across the command layer.
package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/harness/codex"
	"flow/internal/harness/spec"
)

// FallbackName is the harness assumed when a task carries no pin.
//
// tasks.harness is NULL for every row written before the column
// existed, and those sessions were all Claude Code. Resolving empty →
// claude preserves that history; it is a back-compat rule, not a
// preference for claude.
const FallbackName = harness.NameClaude

// natives are the adapters compiled into this binary. Adapters are
// stateless, so this is built once at init rather than per call.
var natives = []harness.Harness{
	claude.New(),
	codex.New(),
}

// ManifestDir is where user-authored harness manifests live.
const ManifestDir = "harnesses"

var (
	loadOnce sync.Once
	resolved []harness.Harness
	loadErrs []error
)

// flowRoot mirrors the app layer's data-root resolution. It is
// duplicated rather than imported because internal/app depends on this
// package, and the rule — $FLOW_ROOT, else ~/.flow — is four lines that
// have never changed.
func flowRoot() (string, error) {
	if r := os.Getenv("FLOW_ROOT"); r != "" {
		return r, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".flow"), nil
}

// load resolves the full harness set once per process: every manifest
// in <flow root>/harnesses/*.toml, then the compiled-in natives.
//
// PRECEDENCE: a user manifest SHADOWS a native adapter of the same
// name. That is deliberate — retuning claude's launch flags should not
// require a new flow binary — and Errors() reports the shadowing so it
// is never silent.
//
// A bad manifest disables ONLY itself. Its error is collected and
// surfaced by `flow harness list`; flow keeps working with the
// harnesses that did load, because a typo in an experimental codex
// manifest must not take out a user's claude sessions.
func load() {
	seen := map[string]bool{}

	root, err := flowRoot()
	if err != nil {
		loadErrs = append(loadErrs, err)
	} else {
		dir := filepath.Join(root, ManifestDir)
		paths, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
		sort.Strings(paths)
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				loadErrs = append(loadErrs, fmt.Errorf("%s: %w", path, err))
				continue
			}
			s, err := spec.Decode(data, path)
			if err != nil {
				loadErrs = append(loadErrs, err)
				continue
			}
			if seen[s.Name] {
				loadErrs = append(loadErrs, fmt.Errorf(
					"%s: harness %q is already defined by an earlier manifest; ignoring this one", path, s.Name))
				continue
			}
			a, err := spec.New(s)
			if err != nil {
				loadErrs = append(loadErrs, err)
				continue
			}
			seen[s.Name] = true
			resolved = append(resolved, a)
		}
	}

	for _, h := range natives {
		name := string(h.Name())
		if seen[name] {
			loadErrs = append(loadErrs, fmt.Errorf(
				"a manifest overrides the built-in %q harness", name))
			continue
		}
		seen[name] = true
		resolved = append(resolved, h)
	}
}

// Reload discards the cached harness set. Tests that write manifests
// into a temp $FLOW_ROOT call it so the next lookup re-reads disk.
func Reload() {
	loadOnce = sync.Once{}
	resolved = nil
	loadErrs = nil
}

// Errors returns the problems hit while loading manifests: unreadable
// files, invalid manifests, duplicate names, and natives shadowed by a
// user manifest. Non-fatal by construction — see load.
func Errors() []error {
	loadOnce.Do(load)
	return loadErrs
}

// All returns every adapter this binary supports: user manifests first,
// then the compiled-in natives they did not shadow.
//
// The returned slice is shared and MUST NOT be mutated by callers —
// it is read on hot paths (flow list's per-row [live] probe) where a
// defensive copy would be pure waste.
func All() []harness.Harness {
	loadOnce.Do(load)
	return resolved
}

// Names returns the comma-joined list of supported harness names, for
// error messages that need to show the user their alternatives.
func Names() string {
	all := All()
	names := make([]string, 0, len(all))
	for _, h := range all {
		names = append(names, string(h.Name()))
	}
	return strings.Join(names, ", ")
}

// Fallback returns the adapter for FallbackName.
func Fallback() harness.Harness {
	all := All()
	for _, h := range all {
		if h.Name() == FallbackName {
			return h
		}
	}
	// Unreachable while claude is compiled in — a manifest may shadow
	// it, but only under the same name. Returning the first adapter
	// beats panicking in a path that just resolves a back-compat
	// default.
	if len(all) > 0 {
		return all[0]
	}
	return natives[0]
}

// ByName looks up an adapter by stored Name.
//
//   - Empty name → (Fallback(), nil). Back-compat for pre-harness-column
//     DB rows where the column is always NULL.
//   - Known non-empty name → (matched adapter, nil).
//   - Unknown non-empty name → (nil, error). Callers decide whether to
//     error out, warn and skip, or coerce. No silent fallback — the
//     "set once on first bind" column semantics break the moment a
//     binary doesn't recognize its own task's pin, and we'd rather
//     refuse than corrupt downstream state by running the wrong adapter.
func ByName(name string) (harness.Harness, error) {
	if name == "" {
		return Fallback(), nil
	}
	for _, h := range All() {
		if string(h.Name()) == name {
			return h, nil
		}
	}
	return nil, fmt.Errorf(
		"task is pinned to harness %q which isn't supported by this flow binary (registered: %s) — upgrade flow, or update tasks.harness via sqlite",
		name, Names(),
	)
}

// Ambient probes the current process env for each known harness's
// session-id env var. Returns the matching adapter if exactly one is
// set; returns nil if none are set OR if several are (defensive — if a
// user nests sessions we'd rather refuse to guess than pick wrong).
func Ambient() harness.Harness {
	var match harness.Harness
	for _, h := range All() {
		// An empty name means the harness publishes no session id, so
		// it can never be the ambient one. Skipping explicitly beats
		// relying on os.Getenv("") happening to return "".
		env := h.SessionIDEnvVar()
		if env == "" || os.Getenv(env) == "" {
			continue
		}
		if match != nil {
			// Two harnesses claim this process. Refuse to guess.
			return nil
		}
		match = h
	}
	return match
}

// Default returns the adapter for code paths with no task context
// (`flow init`, `flow skill install`, the SessionStart hook handler
// before bind). Probes ambient first so a user inside a non-claude
// shell gets the matching install; otherwise the fallback. Always
// returns a concrete adapter — there is no task pin to mis-resolve.
func Default() harness.Harness {
	if h := Ambient(); h != nil {
		return h
	}
	return Fallback()
}
