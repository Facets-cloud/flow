package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withClock pins time.Local and the list command's clock seam for the test.
func withClock(t *testing.T, loc *time.Location, now time.Time) {
	t.Helper()
	oldLocal, oldNow := time.Local, listNow
	time.Local = loc
	listNow = func() time.Time { return now }
	t.Cleanup(func() { time.Local = oldLocal; listNow = oldNow })
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("tzdata %s unavailable: %v", name, err)
	}
	return loc
}

// TestListSinceTimezoneBoundaries: --since cutoffs are local-midnight
// boundaries, and stored timestamps are compared as instants regardless of
// the UTC offset they were written with.
func TestListSinceTimezoneBoundaries(t *testing.T) {
	ist := mustLoc(t, "Asia/Kolkata")
	la := mustLoc(t, "America/Los_Angeles")

	cases := []struct {
		name  string
		loc   *time.Location
		now   time.Time
		since string
		in    []time.Time // must be listed
		out   []time.Time // must NOT be listed
	}{
		{
			name:  "today IST just after midnight",
			loc:   ist,
			now:   time.Date(2026, 11, 11, 0, 30, 0, 0, ist),
			since: "today",
			in:    []time.Time{time.Date(2026, 11, 11, 0, 10, 0, 0, ist)},
			out:   []time.Time{time.Date(2026, 11, 10, 23, 50, 0, 0, ist)},
		},
		{
			name:  "monday IST (Wed) just after midnight",
			loc:   ist,
			now:   time.Date(2026, 11, 11, 0, 30, 0, 0, ist),
			since: "monday",
			in:    []time.Time{time.Date(2026, 11, 9, 0, 10, 0, 0, ist)},
			out:   []time.Time{time.Date(2026, 11, 8, 23, 50, 0, 0, ist)},
		},
		{
			name:  "date IST",
			loc:   ist,
			now:   time.Date(2026, 11, 11, 0, 30, 0, 0, ist),
			since: "2026-11-10",
			in:    []time.Time{time.Date(2026, 11, 10, 0, 10, 0, 0, ist)},
			out:   []time.Time{time.Date(2026, 11, 9, 23, 50, 0, 0, ist)},
		},
		{
			name:  "today LA late evening",
			loc:   la,
			now:   time.Date(2026, 11, 10, 23, 30, 0, 0, la),
			since: "today",
			in:    []time.Time{time.Date(2026, 11, 10, 0, 10, 0, 0, la)},
			out:   []time.Time{time.Date(2026, 11, 9, 23, 50, 0, 0, la)},
		},
		{
			name:  "monday LA late evening",
			loc:   la,
			now:   time.Date(2026, 11, 10, 23, 30, 0, 0, la),
			since: "monday",
			in:    []time.Time{time.Date(2026, 11, 9, 0, 10, 0, 0, la)},
			out:   []time.Time{time.Date(2026, 11, 8, 23, 50, 0, 0, la)},
		},
		{
			name:  "date LA",
			loc:   la,
			now:   time.Date(2026, 11, 10, 23, 30, 0, 0, la),
			since: "2026-11-10",
			in:    []time.Time{time.Date(2026, 11, 10, 0, 10, 0, 0, la)},
			out:   []time.Time{time.Date(2026, 11, 9, 23, 50, 0, 0, la)},
		},
	}
	// Each instant is stored in three encodings: local offset, UTC "Z",
	// and a foreign offset. All must be judged by the instant they denote.
	encodings := map[string]func(time.Time) string{
		"local": func(x time.Time) string { return x.Format(time.RFC3339) },
		"utc":   func(x time.Time) string { return x.UTC().Format(time.RFC3339) },
		"tokyo": func(x time.Time) string {
			return x.In(time.FixedZone("JST", 9*3600)).Format(time.RFC3339)
		},
	}
	for _, c := range cases {
		for encName, enc := range encodings {
			t.Run(c.name+"/"+encName, func(t *testing.T) {
				root, db := showListEditDB(t)
				withClock(t, c.loc, c.now)
				set := func(slug string, ts time.Time) {
					insertTask(t, db, slug, slug, "backlog", "high", filepath.Join(root, "x"), nil)
					if _, err := db.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, enc(ts.In(c.loc)), slug); err != nil {
						t.Fatal(err)
					}
				}
				for i, ts := range c.in {
					set("in-"+string(rune('a'+i)), ts)
				}
				for i, ts := range c.out {
					set("out-"+string(rune('a'+i)), ts)
				}
				out := captureStdout(t, func() {
					if rc := cmdList([]string{"tasks", "--since", c.since}); rc != 0 {
						t.Errorf("rc=%d", rc)
					}
				})
				for i := range c.in {
					if s := "in-" + string(rune('a'+i)); !strings.Contains(out, s) {
						t.Errorf("expected %s listed; out=%q", s, out)
					}
				}
				for i := range c.out {
					if s := "out-" + string(rune('a'+i)); strings.Contains(out, s) {
						t.Errorf("did not expect %s listed; out=%q", s, out)
					}
				}
			})
		}
	}
}
