package server

import "testing"

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
