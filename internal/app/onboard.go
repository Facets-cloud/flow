package app

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
	"flow/internal/harness/codex"
	"flow/internal/harness/prx"
)

// flow onboard — import a new user's past agent sessions as flow tasks
// without spending their model tokens on raw transcripts. All reading,
// rendering and trimming happens here in Go; the agent only ever sees a
// bounded digest (scan) and, for sessions the user picks, a bounded
// per-session digest. Budgets: small fits a $20 plan, large a $100–200 one.

type onboardBudget struct {
	days, clusters, perCluster, digestChars int
}

var onboardBudgets = map[string]onboardBudget{
	"small": {days: 14, clusters: 15, perCluster: 3, digestChars: 6000},
	"large": {days: 60, clusters: 40, perCluster: 3, digestChars: 12000},
}

type onboardSession struct {
	harness, id, path string
	mod               time.Time
}

func (s onboardSession) ref() string { return s.harness + ":" + s.id }

type sessionSummary struct {
	onboardSession
	cwd       string
	prompts   []string // user prompts, noise dropped, each clipped
	lastReply string
	machinery bool // first prompt was flow's own (close-out sweep, auto run)
}

// flowMachinery marks sessions flow itself launched headlessly; their
// work is already tracked (or was a sweep), so they are never candidates.
var flowMachinery = []string{
	"You are running an automated close-out sweep",
	"You are an AUTONOMOUS, headless",
}

func scratchDir(cwd string) bool {
	for _, p := range []string{"/tmp", "/private/tmp", "/var/folders", "/private/var/folders"} {
		if cwd == p || strings.HasPrefix(cwd, p+"/") {
			return true
		}
	}
	return false
}

func cmdOnboard(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: flow onboard scan [--budget small|large] [--days N] [--max N]\n       flow onboard digest <harness:session-id> [--budget small|large] [--chars N]")
		return 2
	}
	switch args[0] {
	case "scan":
		return cmdOnboardScan(args[1:])
	case "digest":
		return cmdOnboardDigest(args[1:])
	}
	fmt.Fprintf(os.Stderr, "error: unknown onboard subcommand %q (want scan|digest)\n", args[0])
	return 2
}

func parseBudget(name string) (onboardBudget, bool) {
	b, ok := onboardBudgets[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "error: --budget must be small or large, got %q\n", name)
	}
	return b, ok
}

func cmdOnboardScan(args []string) int {
	fs := flagSet("onboard scan")
	budget := fs.String("budget", "small", "small ($20 plan) | large ($100-200 plan)")
	days := fs.Int("days", 0, "look back N days (default from budget)")
	max := fs.Int("max", 0, "max directories listed (default from budget)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	b, ok := parseBudget(*budget)
	if !ok {
		return 2
	}
	if *days > 0 {
		b.days = *days
	}
	if *max > 0 {
		b.clusters = *max
	}

	skip := boundSessionIDs()
	since := time.Now().AddDate(0, 0, -b.days)
	type cluster struct {
		cwd      string
		sessions []*sessionSummary
		turns    int
		harness  map[string]int
	}
	byCwd := map[string]*cluster{}
	total := 0
	for _, s := range discoverSessions() {
		if s.mod.Before(since) || skip[s.id] {
			continue
		}
		sum, err := summarizeSession(s)
		if err != nil || len(sum.prompts) == 0 || sum.machinery || scratchDir(sum.cwd) {
			continue // unreadable, empty, flow's own headless runs, or temp dirs: nothing to import
		}
		total++
		c := byCwd[sum.cwd]
		if c == nil {
			c = &cluster{cwd: sum.cwd, harness: map[string]int{}}
			byCwd[sum.cwd] = c
		}
		c.sessions = append(c.sessions, sum)
		c.turns += len(sum.prompts)
		c.harness[s.harness]++
	}
	clusters := make([]*cluster, 0, len(byCwd))
	for _, c := range byCwd {
		sort.Slice(c.sessions, func(i, j int) bool { return c.sessions[i].mod.After(c.sessions[j].mod) })
		clusters = append(clusters, c)
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].sessions[0].mod.After(clusters[j].sessions[0].mod) })

	shown := min(len(clusters), b.clusters)
	fmt.Printf("onboard scan: last %dd, %d unbound sessions in %d dirs (showing %d)\n", b.days, total, len(clusters), shown)
	for _, c := range clusters[:shown] {
		var hs []string
		for h, n := range c.harness {
			hs = append(hs, fmt.Sprintf("%s×%d", h, n))
		}
		sort.Strings(hs)
		gone := ""
		if _, err := os.Stat(c.cwd); os.IsNotExist(err) {
			gone = "  (dir no longer exists)"
		}
		fmt.Printf("\n%s  [%s]  last %s  turns %d%s\n", c.cwd, strings.Join(hs, " "), c.sessions[0].mod.Format(time.DateOnly), c.turns, gone)
		for _, s := range c.sessions[:min(len(c.sessions), b.perCluster)] {
			fmt.Printf("  · %s  (%s)\n", oneLine(s.prompts[0], 110), s.ref())
		}
	}
	if total == 0 {
		fmt.Println("\nno unbound sessions found — skip the import step.")
		return 0
	}
	fmt.Println("\nnext: propose candidate tasks from this list; for the ones the user picks, `flow onboard digest <ref>` (one ref per pick).")
	return 0
}

func cmdOnboardDigest(args []string) int {
	fs := flagSet("onboard digest")
	budget := fs.String("budget", "small", "small | large")
	chars := fs.Int("chars", 0, "cap on user-prompt text (default from budget)")
	ref, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref, rest = args[0], args[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if ref == "" && fs.NArg() > 0 {
		ref = fs.Arg(0)
	}
	b, ok := parseBudget(*budget)
	if !ok {
		return 2
	}
	if *chars > 0 {
		b.digestChars = *chars
	}
	h, id, found := strings.Cut(ref, ":")
	if !found || id == "" {
		fmt.Fprintln(os.Stderr, "error: digest needs <harness:session-id> as printed by `flow onboard scan`")
		return 2
	}
	for _, s := range discoverSessions() {
		if s.harness != h || s.id != id {
			continue
		}
		sum, err := summarizeSession(s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		writeDigest(os.Stdout, sum, b.digestChars)
		return 0
	}
	fmt.Fprintf(os.Stderr, "error: no session %s\n", ref)
	return 1
}

// writeDigest prints a session's user prompts within a char cap. When over
// the cap it keeps the newest prompt (where it ended), then up to 3 opening
// prompts (the intent), then more newest ones, never exceeding the cap;
// a prompt that does not fit is clipped to what is left.
func writeDigest(w io.Writer, s *sessionSummary, capChars int) {
	fmt.Fprintf(w, "session %s  cwd %s  last %s  user turns %d\n\nuser asked:\n",
		s.ref(), s.cwd, s.mod.Format(time.DateOnly), len(s.prompts))
	head, tail := s.prompts, []string(nil)
	if n := len(s.prompts); n > 0 && size(s.prompts) > capChars {
		left := capChars
		take := func(p string) string {
			if len(p) > left { // clip in bytes on a rune boundary, "…" included
				cut := max(left-len("…"), 0)
				for cut > 0 && !utf8.RuneStart(p[cut]) {
					cut--
				}
				p = p[:cut] + "…"
			}
			left -= len(p)
			return p
		}
		newest := take(s.prompts[n-1])
		head = nil
		for i := 0; i < n-1 && i < 3 && left > 0; i++ {
			head = append(head, take(s.prompts[i]))
		}
		for i := n - 2; i >= len(head) && len(s.prompts[i]) <= left; i-- {
			tail = append([]string{take(s.prompts[i])}, tail...)
		}
		tail = append(tail, newest)
	}
	for _, p := range head {
		fmt.Fprintf(w, "- %s\n", p)
	}
	if omitted := len(s.prompts) - len(head) - len(tail); omitted > 0 {
		fmt.Fprintf(w, "- … %d prompts omitted …\n", omitted)
	}
	for _, p := range tail {
		fmt.Fprintf(w, "- %s\n", p)
	}
	if s.lastReply != "" {
		fmt.Fprintf(w, "\nlast assistant reply:\n%s\n", s.lastReply)
	}
}

func size(ps []string) (n int) {
	for _, p := range ps {
		n += len(p)
	}
	return n
}

// discoverSessions lists every transcript on disk for the three harnesses.
// Missing roots are fine: a user may have only one harness installed.
func discoverSessions() []onboardSession {
	var out []onboardSession
	add := func(h, id, p string) {
		if fi, err := os.Stat(p); err == nil {
			out = append(out, onboardSession{harness: h, id: id, path: p, mod: fi.ModTime()})
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		files, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", "*.jsonl"))
		for _, p := range files {
			add("claude", strings.TrimSuffix(filepath.Base(p), ".jsonl"), p)
		}
	}
	if root, err := codex.SessionsRoot(); err == nil {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			// rollout-<timestamp>-<36-char thread id>.jsonl
			name := filepath.Base(p)
			if err == nil && !d.IsDir() && strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") && len(name) > 42 {
				add("codex", name[len(name)-42:len(name)-6], p)
			}
			return nil
		})
	}
	if dir, err := prx.SessionDir(); err == nil {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			for _, f := range []string{"session.jsonl", "session.jsonl.gz"} {
				p := filepath.Join(dir, e.Name(), f)
				if _, err := os.Stat(p); err == nil {
					add("prx", e.Name(), p)
					break
				}
			}
		}
	}
	return out
}

// boundSessionIDs: sessions flow already tracks (any task) plus this very
// session — neither is "past work" to import.
func boundSessionIDs() map[string]bool {
	skip := map[string]bool{}
	for _, v := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "PRAXIS_SESSION_ID"} {
		if id := os.Getenv(v); id != "" {
			skip[id] = true
		}
	}
	dbPath, err := flowDBPath()
	if err != nil {
		return skip
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		return skip
	}
	defer db.Close()
	rows, err := db.Query(`SELECT session_id FROM tasks WHERE session_id IS NOT NULL AND session_id != ''`)
	if err != nil {
		return skip
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			skip[id] = true
		}
	}
	return skip
}

func openTranscript(p string) (io.ReadCloser, error) {
	f, err := os.Open(p)
	if err != nil || !strings.HasSuffix(p, ".gz") {
		return f, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{gz, f}, nil
}

// summarizeSession renders the transcript with the harness's own compact
// renderer (no tool results, no thinking) and keeps only user prompts and
// the final assistant reply.
func summarizeSession(s onboardSession) (*sessionSummary, error) {
	sum := &sessionSummary{onboardSession: s, cwd: sessionCwd(s.path)}
	r, err := openTranscript(s.path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	col := &sectionCollector{on: func(label, body string) {
		switch label {
		case "User":
			if first := strings.TrimSpace(body); len(sum.prompts) == 0 && !sum.machinery {
				for _, m := range flowMachinery {
					if strings.HasPrefix(first, m) {
						sum.machinery = true
					}
				}
			}
			if p := userPrompt(body); p != "" {
				sum.prompts = append(sum.prompts, p)
			}
		case "Assistant":
			if t := strings.TrimSpace(body); t != "" {
				sum.lastReply = t
			}
		}
	}}
	render := map[string]func(io.Reader, bool, io.Writer) error{
		"claude": claude.RenderJSONL, "codex": codex.RenderJSONL, "prx": prx.RenderJSONL,
	}[s.harness]
	if err := render(r, true, col); err != nil {
		return nil, err
	}
	col.close()
	sum.lastReply = oneLine(sum.lastReply, 1200)
	return sum, nil
}

// userPrompt drops what the harness or flow injected as a "user" turn
// (hook context, system reminders, slash-command wrappers, interrupts) and
// clips the rest. ponytail: prefix heuristics; tighten if new injected
// shapes leak into scans.
func userPrompt(body string) string {
	t := strings.TrimSpace(body)
	// Injected wrappers only: a real prompt may start with markup ("<div> …").
	for _, noise := range []string{"<system-reminder", "<command-", "<local-command", "<user-prompt-submit-hook",
		"<task-notification", "<environment_context", "<user_instructions", "[via flow", "[Request interrupted", "Caveat:", "flow session →", "Background job", "Monitor \"",
		"# AGENTS.md instructions", "Previous conversation compacted", "Subagent task-", codex.ProbePrompt} {
		if strings.HasPrefix(t, noise) {
			return ""
		}
	}
	if t == "" {
		return ""
	}
	return oneLine(t, 300)
}

// sessionCwd finds the first cwd recorded in the transcript: claude has it
// on records, codex in session_meta.payload, prx on the session header.
func sessionCwd(p string) string {
	r, err := openTranscript(p)
	if err != nil {
		return "(unknown)"
	}
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for i := 0; i < 200 && sc.Scan(); i++ {
		if !bytes.Contains(sc.Bytes(), []byte(`"cwd"`)) {
			continue
		}
		var rec struct {
			Cwd     string `json:"cwd"`
			Payload struct {
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) == nil {
			if rec.Cwd != "" {
				return rec.Cwd
			}
			if rec.Payload.Cwd != "" {
				return rec.Payload.Cwd
			}
		}
	}
	return "(unknown)"
}

// sectionCollector parses the normalized "─── Label ───" stream every
// harness renderer emits, calling on(label, body) per section.
type sectionCollector struct {
	on    func(label, body string)
	buf   []byte
	label string
	body  strings.Builder
}

func (c *sectionCollector) Write(p []byte) (int, error) {
	c.buf = append(c.buf, p...)
	for {
		i := bytes.IndexByte(c.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		l := string(c.buf[:i])
		c.buf = c.buf[i+1:]
		c.line(l)
	}
}

func (c *sectionCollector) line(l string) {
	if strings.HasPrefix(l, "─── ") && strings.HasSuffix(l, " ───") {
		c.flush()
		c.label = strings.TrimSuffix(strings.TrimPrefix(l, "─── "), " ───")
		return
	}
	c.body.WriteString(l)
	c.body.WriteByte('\n')
}

// close emits the trailing section, including an unterminated last line.
func (c *sectionCollector) close() {
	c.body.Write(c.buf)
	c.buf = nil
	c.flush()
}

func (c *sectionCollector) flush() {
	if c.label != "" {
		c.on(c.label, c.body.String())
	}
	c.label = ""
	c.body.Reset()
}
