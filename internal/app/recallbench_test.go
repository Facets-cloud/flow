package app

import (
	"bufio"
	"encoding/json"
	"flow/internal/flowdb"
	"flow/internal/harness/claude"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// This is the scale benchmark behind review point 6: a synthetic generator
// that stands up ~2,500 tasks (briefs, notes, transcripts — one of them a huge
// "live" transcript) under a temp FLOW_ROOT, and a harness that reports cold
// build, warm-no-change, warm-1-changed and warm-with-growing-live-transcript
// times plus peak RSS.
//
// It is heavy (thousands of files + a multi-hundred-MB transcript), so it is
// gated behind FLOW_BENCH=1 and never runs in the normal `go test ./...`:
//
//	FLOW_BENCH=1 go test ./internal/app/ -run TestRecallScaleBench -v -timeout 30m
//	FLOW_BENCH=1 FLOW_BENCH_TASKS=2500 FLOW_BENCH_LIVE_MB=600 go test ./internal/app/ -run TestRecallScaleBench -v -timeout 30m
//
// Targets (from the brief): warm calls < 1 s at 2.5k tasks (stretch 0.5 s),
// and no call scales with total transcript bytes (the growing-live call must
// stay flat as the 600 MB transcript grows).

var benchVocab = []string{
	"kafka", "partition", "broker", "consumer", "offset", "rebalance", "lag", "replica",
	"terraform", "module", "provider", "state", "backend", "drift", "plan", "apply",
	"invoice", "billing", "refund", "ledger", "currency", "rounding", "tax", "reconcile",
	"oauth", "token", "redirect", "session", "cookie", "scope", "consent", "refresh",
	"grafana", "panel", "latency", "metric", "dashboard", "alert", "threshold", "query",
	"postgres", "replication", "vacuum", "index", "wal", "checkpoint", "shard", "failover",
	"kubernetes", "pod", "ingress", "service", "deployment", "scale", "node", "namespace",
	"migration", "cutover", "dms", "snapshot", "rollback", "downtime", "endpoint", "cutover",
}

func benchWords(rng *rand.Rand, n int) string {
	w := make([]byte, 0, n*8)
	for i := 0; i < n; i++ {
		if i > 0 {
			w = append(w, ' ')
		}
		w = append(w, benchVocab[rng.Intn(len(benchVocab))]...)
	}
	return string(w)
}

// genBenchCorpus builds a synthetic flow root of nTasks tasks under a fresh
// temp FLOW_ROOT (and temp HOME for the claude transcripts). One task gets a
// ~liveMB transcript. Returns the designated live task's slug and the path to
// its claude transcript (for the growing-live scenario).
func genBenchCorpus(t *testing.T, nTasks, liveMB int) (liveSlug, liveTranscript string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	root := filepath.Join(tmp, "flow")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "flow.db")
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(tmp, "repo")
	os.MkdirAll(workDir, 0o755)
	claudeDir := filepath.Join(tmp, ".claude", "projects", claude.EncodeCwd(workDir))
	os.MkdirAll(claudeDir, 0o755)

	rng := rand.New(rand.NewSource(42))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO tasks
		(slug,name,status,kind,priority,work_dir,session_id,session_started,harness,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?, 'claude', ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	liveSlug = "live-task-0000"
	start := time.Now()
	for i := 0; i < nTasks; i++ {
		slug := fmt.Sprintf("task-%04d", i)
		if i == 0 {
			slug = liveSlug
		}
		sid := fmt.Sprintf("%08d-0000-4000-8000-%012d", i, i)
		created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		if _, err := stmt.Exec(slug, "Work on "+benchWords(rng, 3), "in-progress", "regular", "medium",
			workDir, sid, created, created, created); err != nil {
			t.Fatal(err)
		}
		// On-disk brief + a couple of update notes.
		tdir := filepath.Join(root, "tasks", slug)
		os.MkdirAll(filepath.Join(tdir, "updates"), 0o755)
		os.WriteFile(filepath.Join(tdir, "brief.md"),
			[]byte("# "+slug+"\n\n## Goal\n"+benchWords(rng, 40)+"\n\n## Notes\n"+benchWords(rng, 30)), 0o644)
		for u := 0; u < 2; u++ {
			os.WriteFile(filepath.Join(tdir, "updates", fmt.Sprintf("2026-02-%02d-note.md", u+1)),
				[]byte(benchWords(rng, 60)), 0o644)
		}
		// Claude transcript with a handful of user prompts.
		tp := filepath.Join(claudeDir, sid+".jsonl")
		if i == 0 && liveMB > 0 {
			writeLiveTranscript(t, tp, rng, int64(liveMB)<<20)
			liveTranscript = tp
		} else {
			writeSmallTranscript(t, tp, rng)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Logf("generated %d tasks in %s (live transcript %d MB)", nTasks, time.Since(start).Round(time.Millisecond), liveMB)
	return liveSlug, liveTranscript
}

func writeSmallTranscript(t *testing.T, path string, rng *rand.Rand) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	for i := 0; i < 8; i++ {
		writeJSONL(bw, "user", benchWords(rng, 12))
		writeJSONL(bw, "assistant", benchWords(rng, 40))
	}
	bw.Flush()
}

func writeLiveTranscript(t *testing.T, path string, rng *rand.Rand, targetBytes int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	// A few genuine user prompts up front, then bulk assistant/tool output to
	// reach the target size (mirrors a real long live session).
	for i := 0; i < 10; i++ {
		writeJSONL(bw, "user", benchWords(rng, 12))
	}
	big := benchWords(rng, 400)
	var written int64
	for written < targetBytes {
		n, _ := writeJSONLn(bw, "assistant", big)
		written += int64(n)
	}
	bw.Flush()
}

func writeJSONL(bw *bufio.Writer, role, text string) {
	writeJSONLn(bw, role, text)
}

func writeJSONLn(bw *bufio.Writer, role, text string) (int, error) {
	var content any = text
	if role == "assistant" {
		content = []map[string]any{{"type": "text", "text": text}}
	}
	b, _ := json.Marshal(map[string]any{
		"type":      role,
		"timestamp": "2026-02-01T00:00:00Z",
		"message":   map[string]any{"role": role, "content": content},
	})
	b = append(b, '\n')
	return bw.Write(b)
}

func maxRSSMB() float64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.Sys) / (1 << 20)
}

// peakRSSMB returns the process's peak resident set size in MB (ru_maxrss).
// darwin reports bytes, linux reports kilobytes.
func peakRSSMB() float64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	b := float64(ru.Maxrss)
	if runtime.GOOS == "linux" {
		b *= 1024
	}
	return b / (1 << 20)
}

// buildIndexOnce runs one full index-maintenance cycle (scan + cached
// hydrate), the per-call work the reviewer measured. Returns elapsed and
// whether the cache was warm.
func buildIndexOnce(t *testing.T) (time.Duration, bool) {
	t.Helper()
	start := time.Now()
	rows, err := matchScan()
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, warm := hydrateCachedWith(rows, loadRawCache())
	return time.Since(start), warm
}

func TestRecallScaleBench(t *testing.T) {
	if os.Getenv("FLOW_BENCH") == "" {
		t.Skip("set FLOW_BENCH=1 to run the scale benchmark")
	}
	nTasks := envInt("FLOW_BENCH_TASKS", 2500)
	liveMB := envInt("FLOW_BENCH_LIVE_MB", 600)

	liveSlug, liveTranscript := genBenchCorpus(t, nTasks, liveMB)
	_ = liveSlug

	// Cold build (no cache yet): the one-time full build.
	cold, _ := buildIndexOnce(t)
	coldRSS := maxRSSMB()

	// Warm, nothing changed: a pure cache hit.
	warmNoChange, warm := buildIndexOnce(t)
	if !warm {
		t.Fatal("expected a warm cache on the second call")
	}

	// Warm, one task changed: rewrite one brief so its fingerprint drifts.
	root, _ := flowRoot()
	changedBrief := filepath.Join(root, "tasks", "task-0007", "brief.md")
	os.WriteFile(changedBrief, []byte("# task-0007\n\n## Goal\nchanged "+benchWords(rand.New(rand.NewSource(7)), 40)), 0o644)
	warm1Changed, _ := buildIndexOnce(t)

	// Warm, the live transcript GREW: append ~50 MB to the 600 MB transcript.
	// This must NOT read the transcript (the task is "changed" → light hydrate),
	// so the time must stay flat regardless of the transcript's size.
	growLive := time.Duration(0)
	grownRSS := 0.0
	if liveTranscript != "" {
		appendToTranscript(t, liveTranscript, 50<<20)
		growLive, _ = buildIndexOnce(t)
		grownRSS = maxRSSMB()
	}

	t.Logf("\n=== recall scale benchmark (%d tasks, %d MB live transcript) ===", nTasks, liveMB)
	t.Logf("%-32s %10s", "phase", "time")
	t.Logf("%-32s %10s", "cold build (first run)", cold.Round(time.Millisecond))
	t.Logf("%-32s %10s", "warm, no change", warmNoChange.Round(time.Millisecond))
	t.Logf("%-32s %10s", "warm, 1 task changed", warm1Changed.Round(time.Millisecond))
	t.Logf("%-32s %10s", "warm, live transcript grew", growLive.Round(time.Millisecond))
	t.Logf("Go Sys: cold=%.0f MB after-grow=%.0f MB", coldRSS, grownRSS)
	t.Logf("peak process RSS: %.0f MB", peakRSSMB())

	// Assert the targets so the bench fails loudly on a regression.
	if warmNoChange > time.Second {
		t.Errorf("warm no-change %s exceeds 1s target", warmNoChange)
	}
	if warm1Changed > time.Second {
		t.Errorf("warm 1-changed %s exceeds 1s target", warm1Changed)
	}
	if growLive > time.Second {
		t.Errorf("warm growing-live %s exceeds 1s target (call scaled with transcript bytes!)", growLive)
	}
}

func appendToTranscript(t *testing.T, path string, addBytes int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	rng := rand.New(rand.NewSource(99))
	big := benchWords(rng, 400)
	var written int64
	for written < addBytes {
		n, _ := writeJSONLn(bw, "assistant", big)
		written += int64(n)
	}
	bw.Flush()
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
