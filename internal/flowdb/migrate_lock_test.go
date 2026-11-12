package flowdb

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Review note 6 on #132: the migration flock had no timeout, so an opener
// behind a stuck migrator hung forever with no message.
func TestLockMigrationsTimesOutWithClearError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	unlock, err := lockMigrations(path)
	if err != nil {
		t.Fatal(err)
	}

	orig := migrationLockTimeout
	migrationLockTimeout = 150 * time.Millisecond
	t.Cleanup(func() { migrationLockTimeout = orig })

	start := time.Now()
	if unlock2, err := lockMigrations(path); err == nil {
		unlock2()
		t.Fatal("second lockMigrations succeeded while the lock was held")
	} else {
		for _, want := range []string{"timed out", path + ".migrate.lock"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("waited %s, want about the 150ms timeout", waited)
	}

	// Released: the next opener gets it straight away.
	unlock()
	unlock3, err := lockMigrations(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock3()
}
