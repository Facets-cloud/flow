package flowdb

import (
	"path/filepath"
	"testing"
)

func TestMigrationAddsParentSlug(t *testing.T) {
	db := openTempDB(t)
	has, err := columnExists(db, "tasks", "parent_slug")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("tasks.parent_slug should exist on a fresh DB")
	}
}

// A DB whose tasks table predates parent_slug gets it via ALTER on open.
func TestMigrationAddsParentSlugToOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE tasks DROP COLUMN parent_slug`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	db.Close()

	db, err = OpenDB(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if has, _ := columnExists(db, "tasks", "parent_slug"); !has {
		t.Fatal("parent_slug should be re-added by migration")
	}
}

// Some live DBs already carry legacy, unused parent_slug and
// forked_from_slug columns. Opening must not try to re-add parent_slug
// and must leave forked_from_slug alone.
func TestMigrationParentSlugLegacyColumnsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE tasks DROP COLUMN parent_slug`,
		`ALTER TABLE tasks ADD COLUMN parent_slug TEXT REFERENCES tasks(slug)`,
		`ALTER TABLE tasks ADD COLUMN forked_from_slug TEXT`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	for i := 0; i < 2; i++ {
		db, err = OpenDB(path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		db.Close()
	}
	db, _ = OpenDB(path)
	defer db.Close()
	if has, _ := columnExists(db, "tasks", "forked_from_slug"); !has {
		t.Error("forked_from_slug must be left alone")
	}
}

func TestParentSlugRoundTripAndChildren(t *testing.T) {
	db := openTempDB(t)
	now := NowISO()
	wd := t.TempDir()
	ins := `INSERT INTO tasks (slug, name, status, priority, work_dir, parent_slug, created_at, updated_at)
	        VALUES (?, ?, 'backlog', 'medium', ?, ?, ?, ?)`
	if _, err := db.Exec(ins, "parent", "Parent", wd, nil, now, now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"kid-b", "kid-a"} {
		if _, err := db.Exec(ins, c, c, wd, "parent", now, now); err != nil {
			t.Fatal(err)
		}
	}

	kid, err := GetTask(db, "kid-a")
	if err != nil {
		t.Fatal(err)
	}
	if !kid.ParentSlug.Valid || kid.ParentSlug.String != "parent" {
		t.Errorf("ParentSlug = %+v, want parent", kid.ParentSlug)
	}
	p, _ := GetTask(db, "parent")
	if p.ParentSlug.Valid {
		t.Errorf("parent should have no ParentSlug, got %q", p.ParentSlug.String)
	}

	kids, err := ChildTasks(db, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 2 || kids[0] != "kid-a" || kids[1] != "kid-b" {
		t.Errorf("ChildTasks = %v, want [kid-a kid-b]", kids)
	}
}
