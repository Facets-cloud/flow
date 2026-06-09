package flowdb

import (
	"database/sql"
	"testing"
)

func TestCreateAndGetOwner(t *testing.T) {
	db := openTempDB(t)

	o := &Owner{
		Slug:    "af-maint",
		Name:    "agent-factory maintenance",
		WorkDir: "/Users/anshulsao/code/agent-factory",
		Every:   "30m",
	}
	if err := CreateOwner(db, o); err != nil {
		t.Fatalf("CreateOwner: %v", err)
	}

	got, err := GetOwner(db, "af-maint")
	if err != nil {
		t.Fatalf("GetOwner: %v", err)
	}
	if got.Name != "agent-factory maintenance" {
		t.Errorf("Name = %q, want %q", got.Name, "agent-factory maintenance")
	}
	if got.WorkDir != "/Users/anshulsao/code/agent-factory" {
		t.Errorf("WorkDir = %q", got.WorkDir)
	}
	if got.Every != "30m" {
		t.Errorf("Every = %q, want 30m", got.Every)
	}
	if got.Status != "active" {
		t.Errorf("Status = %q, want default active", got.Status)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Errorf("timestamps not set: created=%q updated=%q", got.CreatedAt, got.UpdatedAt)
	}
	if got.NextWakeAt.Valid {
		t.Errorf("NextWakeAt should be NULL on a freshly created (un-started) owner, got %q", got.NextWakeAt.String)
	}
}

func TestListOwnersOrdersAndFilters(t *testing.T) {
	db := openTempDB(t)

	for _, o := range []*Owner{
		{Slug: "ccc", Name: "C", WorkDir: "/c", Every: "30m"},
		{Slug: "aaa", Name: "A", WorkDir: "/a", Every: "1h"},
		{Slug: "bbb", Name: "B", WorkDir: "/b", Every: "1h", Status: "paused"},
	} {
		if err := CreateOwner(db, o); err != nil {
			t.Fatalf("CreateOwner %s: %v", o.Slug, err)
		}
	}
	// Archive one so the default list should exclude it.
	if _, err := db.Exec(`UPDATE owners SET archived_at = ? WHERE slug = 'ccc'`, NowISO()); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Default: non-archived, sorted by slug.
	got, err := ListOwners(db, OwnerFilter{})
	if err != nil {
		t.Fatalf("ListOwners: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("default list len = %d, want 2 (ccc archived)", len(got))
	}
	if got[0].Slug != "aaa" || got[1].Slug != "bbb" {
		t.Errorf("order = [%s %s], want [aaa bbb]", got[0].Slug, got[1].Slug)
	}

	// Status filter.
	paused, err := ListOwners(db, OwnerFilter{Status: "paused"})
	if err != nil {
		t.Fatalf("ListOwners(paused): %v", err)
	}
	if len(paused) != 1 || paused[0].Slug != "bbb" {
		t.Errorf("paused filter = %v, want [bbb]", paused)
	}

	// IncludeArchived surfaces the archived owner too.
	all, err := ListOwners(db, OwnerFilter{IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListOwners(all): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("IncludeArchived list len = %d, want 3", len(all))
	}
}

func TestDueOwnersReturnsOnlyActivePastDue(t *testing.T) {
	db := openTempDB(t)

	const now = "2026-06-08T12:00:00Z"
	past := sql.NullString{String: "2026-06-08T11:00:00Z", Valid: true}
	future := sql.NullString{String: "2026-06-08T13:00:00Z", Valid: true}

	owners := []*Owner{
		{Slug: "due-now", Name: "n", WorkDir: "/x", Every: "1h", Status: "active", NextWakeAt: past},
		{Slug: "future", Name: "n", WorkDir: "/x", Every: "1h", Status: "active", NextWakeAt: future},
		{Slug: "never-started", Name: "n", WorkDir: "/x", Every: "1h", Status: "active"}, // NextWakeAt NULL
		{Slug: "paused-due", Name: "n", WorkDir: "/x", Every: "1h", Status: "paused", NextWakeAt: past},
		{Slug: "archived-due", Name: "n", WorkDir: "/x", Every: "1h", Status: "active", NextWakeAt: past},
	}
	for _, o := range owners {
		if err := CreateOwner(db, o); err != nil {
			t.Fatalf("CreateOwner %s: %v", o.Slug, err)
		}
	}
	if _, err := db.Exec(`UPDATE owners SET archived_at = ? WHERE slug = 'archived-due'`, now); err != nil {
		t.Fatalf("archive: %v", err)
	}

	due, err := DueOwners(db, now)
	if err != nil {
		t.Fatalf("DueOwners: %v", err)
	}
	if len(due) != 1 || due[0].Slug != "due-now" {
		var slugs []string
		for _, o := range due {
			slugs = append(slugs, o.Slug)
		}
		t.Fatalf("DueOwners = %v, want only [due-now]", slugs)
	}
}

func TestDueOwnersHandlesMixedTimezoneOffsets(t *testing.T) {
	db := openTempDB(t)

	// next_wake stored with a +05:30 offset == 18:25:00 UTC. It IS due when
	// "now" is 18:30:00Z, but a naive string comparison ("23:55…+05:30" >
	// "18:30…Z") would wrongly skip it. Comparison must be timezone-correct.
	wake := sql.NullString{String: "2026-06-08T23:55:00+05:30", Valid: true}
	if err := CreateOwner(db, &Owner{
		Slug: "o", Name: "O", WorkDir: "/x", Every: "30m", Status: "active", NextWakeAt: wake,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := DueOwners(db, "2026-06-08T18:30:00Z")
	if err != nil {
		t.Fatalf("DueOwners: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("owner due at 18:25Z must be returned when now=18:30Z (mixed offsets); got %d", len(got))
	}
}

func TestOwnerTickFieldsPersist(t *testing.T) {
	db := openTempDB(t)
	o := &Owner{Slug: "o", Name: "O", WorkDir: "/x", Every: "30m"}
	if err := CreateOwner(db, o); err != nil {
		t.Fatal(err)
	}
	// A freshly created owner has no tick running.
	if o.TickPID.Valid {
		t.Errorf("new owner should have NULL tick_pid")
	}

	o.TickPID = sql.NullInt64{Int64: 4242, Valid: true}
	o.TickStarted = sql.NullString{String: "2026-06-09T00:00:00Z", Valid: true}
	if err := UpdateOwner(db, o); err != nil {
		t.Fatal(err)
	}
	got, err := GetOwner(db, "o")
	if err != nil {
		t.Fatal(err)
	}
	if got.TickPID.Int64 != 4242 {
		t.Errorf("TickPID = %+v, want 4242", got.TickPID)
	}
	if got.TickStarted.String != "2026-06-09T00:00:00Z" {
		t.Errorf("TickStarted = %+v", got.TickStarted)
	}
}

func TestUpdateOwnerPersistsMutableFields(t *testing.T) {
	db := openTempDB(t)

	o := &Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m"}
	if err := CreateOwner(db, o); err != nil {
		t.Fatalf("CreateOwner: %v", err)
	}

	o.Status = "paused"
	o.NextWakeAt = sql.NullString{String: "2026-06-08T13:00:00Z", Valid: true}
	o.LastTickAt = sql.NullString{String: "2026-06-08T12:00:00Z", Valid: true}
	o.LastTickStatus = sql.NullString{String: "ok", Valid: true}
	if err := UpdateOwner(db, o); err != nil {
		t.Fatalf("UpdateOwner: %v", err)
	}

	got, err := GetOwner(db, "o1")
	if err != nil {
		t.Fatalf("GetOwner: %v", err)
	}
	if got.Status != "paused" {
		t.Errorf("Status = %q, want paused", got.Status)
	}
	if got.NextWakeAt.String != "2026-06-08T13:00:00Z" {
		t.Errorf("NextWakeAt = %q", got.NextWakeAt.String)
	}
	if got.LastTickAt.String != "2026-06-08T12:00:00Z" {
		t.Errorf("LastTickAt = %q", got.LastTickAt.String)
	}
	if got.LastTickStatus.String != "ok" {
		t.Errorf("LastTickStatus = %q, want ok", got.LastTickStatus.String)
	}
	if got.UpdatedAt == "" {
		t.Errorf("UpdatedAt not set")
	}
}
