// Package match learns, from a user's own flow history, whether a new
// prompt belongs to a given task. It ships the technique, not a model:
// every user trains on their own $FLOW_ROOT data and the model file is
// written locally (never committed, never embedded).
//
// The model is a sparse TF-IDF vector space with one document per task
// (name + brief + update notes + that task's past user prompts), whose
// document-frequency statistics are maintained exactly under add /
// replace / remove — so an incremental update is mathematically the
// same index as a full retrain. A small logistic layer over similarity
// features (own-task cosine, margin over the best competing task, rank,
// query length) turns the raw similarity into a calibrated "belongs"
// probability with a learned threshold.
package match

import (
	"regexp"
	"strings"
	"unicode"
)

// ---------- prompt cleaning ----------

// dropPrefixes are user-turn openers that are never the human typing a
// prompt: harness/flow-injected payloads, hook feedback, slash-command
// echo, interruption markers, bus deliveries, bootstrap prompts.
var dropPrefixes = []string{
	"<task-notification>",
	"stop hook feedback:",
	"[request interrupted",
	"base directory for this skill:",
	"this session is being continued from",
	"you are an autonomous",
	"you are the execution session",
	"you are running inside a flow",
	"another claude session sent",
	"another session sent",
	"<local-command-",
	"<command-name>",
	"<command-message>",
	"<command-args>",
	"a session-scoped stop hook",
	"goal check-in:",
	"(re-invocation of",
	"<artifact-content",
	"caveat:",
	"<bash-stdout>",
	"<bash-stderr>",
	"[usage limit reached",
	"[earlier usage-limit",
	"# agents.md instructions",
	"<environment_context>",
	"<user_instructions>",
	"<permissions instructions>",
	"[flow-upgrade]",
	"[flow-bus]",
	"sessionstart hook",
	"userpromptsubmit hook",
	"# claude in chrome",
	"# workflow authoring reference",
	"## page contract",
	"<turn_aborted>",
	"<user_shell_command>",
}

// keepPrefixes mark injected-but-genuine instructions: strip the marker,
// keep the instruction text.
var keepPrefixes = []string{
	"[via flow do --with]",
}

var (
	// Paired tag blocks injected into user turns (system reminders, hook
	// context, pasted-content wrappers keep their body).
	reTagBlock = regexp.MustCompile(`(?s)<(system-reminder|user-prompt-submit-hook|hook-context|command-stdout|local-command-stdout|bash-stdout|bash-stderr|environment_context)[^>]*>.*?</(system-reminder|user-prompt-submit-hook|hook-context|command-stdout|local-command-stdout|bash-stdout|bash-stderr|environment_context)>`)
	reAnyTag   = regexp.MustCompile(`</?[a-zA-Z_][a-zA-Z0-9_-]*(\s[^>]*)?>`)
	reImage    = regexp.MustCompile(`\[Image[^\]]*\]`)
)

// CleanPrompt strips flow/hook/harness boilerplate from one raw user
// turn. ok=false means the whole turn is boilerplate (or empty after
// cleaning) and must not be used as a prompt.
func CleanPrompt(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	low := strings.ToLower(s)
	for _, p := range dropPrefixes {
		if strings.HasPrefix(low, p) {
			return "", false
		}
	}
	for _, p := range keepPrefixes {
		if strings.HasPrefix(low, p) {
			s = strings.TrimSpace(s[len(p):])
			break
		}
	}
	s = reTagBlock.ReplaceAllString(s, " ")
	s = reAnyTag.ReplaceAllString(s, " ")
	s = reImage.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	// A turn that, after stripping, still opens with a known payload
	// (e.g. a reminder block followed by a skill body) is boilerplate.
	low = strings.ToLower(s)
	for _, p := range dropPrefixes {
		if strings.HasPrefix(low, p) {
			return "", false
		}
	}
	return s, true
}

// IsInjectedPrompt reports whether a raw user turn is entirely harness/flow
// boilerplate — a hook-context block, a skill body, a <task-notification>, a
// bus delivery, a bootstrap prompt — rather than genuine human input. It is
// the inverse of CleanPrompt's ok and shares the same dropPrefixes list, so
// recall and the transcript store agree on what counts as a real user turn
// from a SINGLE source of truth. A turn that merely carries an injected
// block alongside real typed text (e.g. a system-reminder prepended to a
// genuine prompt) is NOT injected: CleanPrompt strips the block and keeps the
// text, so it still counts as a user turn.
func IsInjectedPrompt(raw string) bool {
	_, ok := CleanPrompt(raw)
	return !ok
}

// userHeader is the section label every harness renderer (claude, codex,
// prx) emits for a user turn in RenderTranscript's normalized output.
const userHeader = "─── User ───"

// SplitUserTurns extracts the user-turn bodies from RenderTranscript's
// normalized, harness-agnostic text rendering.
func SplitUserTurns(rendered string) []string {
	var out []string
	var cur []string
	in := false
	flush := func() {
		if in {
			out = append(out, strings.TrimSpace(strings.Join(cur, "\n")))
		}
		cur = cur[:0]
	}
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "─── ") && strings.HasSuffix(line, " ───") {
			flush()
			in = line == userHeader
			continue
		}
		if in {
			cur = append(cur, line)
		}
	}
	flush()
	return out
}

// ExtractPrompts = SplitUserTurns + CleanPrompt, dropping boilerplate.
func ExtractPrompts(rendered string) []string {
	var out []string
	for _, t := range SplitUserTurns(rendered) {
		if c, ok := CleanPrompt(t); ok {
			out = append(out, c)
		}
	}
	return out
}

// ---------- tokenization ----------

// MaxPromptTokens caps how many tokens one prompt contributes, so a
// pasted log can't dominate a task's representation.
const MaxPromptTokens = 400

var stopwords = func() map[string]bool {
	words := `a about above after again against all am an and any are as at be because been before being
below between both but by can cannot could did do does doing done down during each few for from further
had has have having he her here hers herself him himself his how i if in into is it its itself let lets me
more most my myself no nor not of off on once only or other ought our ours ourselves out over own same she
should so some such than that the their theirs them themselves then there these they this those through to
too under until up very was we were what when where which while who whom why will with would you your yours
yourself yourselves im ive dont doesnt didnt isnt wasnt cant wont thats whats theres also just now ok okay
yes yeah yep no nope please pls thanks thank go do get got make made want need like see look let us use
using used one two way thing things something anything sure right well still yet ever even much many may
might must shall via per etc ie eg vs re say said tell know think try mean http https www com org html
md txt json yaml yml true false null nil s t d ll m o ve y`
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}()

// reHexish matches ids: long hex runs (shas, uuids) and short hex
// fragments that mix digits and letters (uuid groups like "4a11").
var (
	reHexish  = regexp.MustCompile(`^[0-9a-f]{7,}$`)
	reHexFrag = regexp.MustCompile(`^[0-9a-f]{4,6}$`)
)

func idLike(w string) bool {
	if reHexish.MatchString(w) {
		return true
	}
	return reHexFrag.MatchString(w) && strings.ContainsAny(w, "0123456789") && strings.ContainsAny(w, "abcdef")
}

// Tokenize lowercases, splits on non-alphanumerics, keeps short
// slug-like compounds (e.g. "prompt-task-match") as an extra token,
// drops stopwords / numbers / hex ids, and applies a light stemmer.
func Tokenize(s string) []string {
	var out []string
	emit := func(w string) {
		w = strings.Trim(w, "-_./:")
		if len(w) < 2 || stopwords[w] || isNumeric(w) || idLike(w) {
			return
		}
		out = append(out, stem(w))
	}
	coarse := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' || r == '/' || r == ':')
	})
	for _, c := range coarse {
		c = strings.Trim(c, "-_./:")
		if c == "" {
			continue
		}
		parts := strings.FieldsFunc(c, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r)) })
		anyID := false
		for _, p := range parts {
			anyID = anyID || idLike(p) || isNumeric(p)
		}
		if !anyID && len(parts) > 1 && len(parts) <= 4 && len(c) <= 40 && !strings.Contains(c, "/") && !strings.Contains(c, ":") {
			// slug / identifier compound — a strong task signal on its own.
			out = append(out, strings.Join(parts, "-"))
		}
		for _, p := range parts {
			emit(p)
		}
	}
	return out
}

func isNumeric(w string) bool {
	for _, r := range w {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// stem is a deliberately light suffix stripper (plural / -ing / -ed);
// enough to merge "deploys/deploying/deployed" without Porter's cost.
func stem(w string) string {
	n := len(w)
	switch {
	case n > 5 && strings.HasSuffix(w, "ies"):
		return w[:n-3] + "y"
	case n > 5 && strings.HasSuffix(w, "ing"):
		return w[:n-3]
	case n > 4 && strings.HasSuffix(w, "ed") && !strings.HasSuffix(w, "eed"):
		return w[:n-2]
	case n > 4 && strings.HasSuffix(w, "es") && (strings.HasSuffix(w, "sses") || strings.HasSuffix(w, "xes") || strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes")):
		return w[:n-2]
	case n > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		return w[:n-1]
	}
	return w
}

// Counts tokenizes s into a term-frequency map, keeping at most limit
// tokens (limit <= 0 means unlimited). Adjacent content words also emit
// a bigram term ("logical replication") so phrases outscore documents
// that merely contain both words far apart. Bigram terms contain a
// space; unigram tokens never do.
func Counts(s string, limit int) map[string]float64 {
	toks := Tokenize(s)
	if limit > 0 && len(toks) > limit {
		toks = toks[:limit]
	}
	m := make(map[string]float64, 2*len(toks))
	prev := ""
	for _, t := range toks {
		m[t]++
		if strings.Contains(t, "-") {
			// slug compound: its parts follow and form their own bigrams.
			prev = ""
			continue
		}
		if prev != "" {
			m[prev+" "+t]++
		}
		prev = t
	}
	return m
}

// IsBigram reports whether a term is a bigram emitted by Counts.
func IsBigram(t string) bool { return strings.Contains(t, " ") }
