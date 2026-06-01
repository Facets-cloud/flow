package server

import (
	"bytes"
	"testing"
)

func TestCompleteUTF8PrefixCarriesSplitRune(t *testing.T) {
	input := []byte("hello ")
	input = append(input, []byte("★")[:2]...)

	ready, pending := completeUTF8Prefix(input)
	if string(ready) != "hello " {
		t.Fatalf("ready = %q", string(ready))
	}
	if len(pending) != 2 {
		t.Fatalf("pending len = %d", len(pending))
	}

	ready, pending = completeUTF8Prefix(append(pending, []byte("★")[2:]...))
	if string(ready) != "★" || len(pending) != 0 {
		t.Fatalf("ready=%q pending=%q", string(ready), string(pending))
	}
}

func TestCompleteUTF8PrefixReplacesInvalidBytes(t *testing.T) {
	ready, pending := completeUTF8Prefix([]byte{'o', 'k', ' ', 0xff})
	if string(ready) != "ok \uFFFD" {
		t.Fatalf("ready = %q", string(ready))
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %q", string(pending))
	}
}

// TokensUsed is the last turn's full total (context occupancy, incl. cache);
// TokensSession is cumulative NEW work, EXCLUDING cache re-reads.
func TestAccumulateTranscriptUsageSumsClaudeSession(t *testing.T) {
	var stats transcriptUsageStats
	accumulateTranscriptUsage(&stats, []byte(`{"type":"assistant","message":{"model":"claude","usage":{"input_tokens":10,"cache_read_input_tokens":1000,"output_tokens":20}}}`))
	accumulateTranscriptUsage(&stats, []byte(`{"type":"assistant","message":{"model":"claude","usage":{"input_tokens":5,"cache_read_input_tokens":1100,"output_tokens":30}}}`))
	if stats.TokensUsed != 1135 { // context = last turn total: 5+1100+30
		t.Fatalf("TokensUsed = %d, want 1135 (context = last turn)", stats.TokensUsed)
	}
	// freshTotal per turn excludes cache_read: (10+20) + (5+30) = 65 (not 2165).
	if stats.TokensSession != 65 {
		t.Fatalf("TokensSession = %d, want 65 (cumulative, cache-reads excluded)", stats.TokensSession)
	}
}

// Codex bundles cached tokens into input_tokens (exposed as cached_input_tokens);
// session usage subtracts that, context tracks last_token_usage.
func TestAccumulateTranscriptUsageCodexTotal(t *testing.T) {
	var stats transcriptUsageStats
	accumulateTranscriptUsage(&stats, []byte(`{"payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":50},"total_token_usage":{"input_tokens":9000,"cached_input_tokens":8000,"output_tokens":1000},"model_context_window":272000}}}`))
	if stats.TokensUsed != 150 { // last_token_usage: 100+50
		t.Fatalf("TokensUsed = %d, want 150 (context)", stats.TokensUsed)
	}
	// freshTotal of total_token_usage: (9000-8000) + 1000 = 2000 (cache excluded).
	if stats.TokensSession != 2000 {
		t.Fatalf("TokensSession = %d, want 2000 (session, cache excluded)", stats.TokensSession)
	}
	if stats.TokensMax != 272000 {
		t.Fatalf("TokensMax = %d, want 272000", stats.TokensMax)
	}
}

// When scrollback overflows the cap, the trim must advance to a line boundary so
// a reconnect's replay never begins mid-line or mid-escape-sequence (a byte-
// offset slice could otherwise land inside a CSI like "\x1b[3"|"2m" and corrupt
// the client terminal's parser for the rest of the replay).
func TestTrimScrollbackToLineBoundary(t *testing.T) {
	// 32-byte lines, each starting with an SGR sequence so a naive byte-offset
	// cut would have a high chance of landing inside one.
	body := append([]byte("\x1b[32m"), bytes.Repeat([]byte("x"), 26)...)
	line := append(body, '\n') // 32 bytes
	if len(line) != 32 {
		t.Fatalf("test line len = %d, want 32", len(line))
	}
	var buf []byte
	for i := 0; i < 100; i++ {
		buf = append(buf, line...) // 3200 bytes total
	}

	// Cap that lands mid-line (1000 is not a multiple of 32) \u2014 the trim must
	// advance past the next newline rather than slicing inside a line/sequence.
	got := trimScrollbackToLineBoundary(buf, 1000)
	if len(got) > 1000 {
		t.Fatalf("trimmed len %d exceeds cap 1000", len(got))
	}
	if len(got)%len(line) != 0 {
		t.Fatalf("trimmed len %d not aligned to %d-byte line boundary", len(got), len(line))
	}
	if !bytes.HasPrefix(got, body) {
		t.Fatalf("trimmed buffer does not start at a clean line boundary: %q\u2026", got[:min(8, len(got))])
	}
	// Under the cap \u2192 returned unchanged.
	if got := trimScrollbackToLineBoundary(line, 1000); len(got) != len(line) {
		t.Fatalf("under-cap buffer was trimmed: %d != %d", len(got), len(line))
	}
}
