package flowdb

import (
	"path/filepath"
	"testing"
)

// bus_messages.attachments: a JSON array of {name,size,sha256}; NULL when
// the row carries none.
func TestBusAttachmentsRoundtrip(t *testing.T) {
	db := openBusTestDB(t)
	with := &BusMessage{ID: "att00001", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "user", ToAssignee: "user", Body: "see file",
		Attachments: []Attachment{{Name: "a.txt", Size: 3, SHA256: "abc"}, {Name: "b.png", Size: 9, SHA256: "def"}}}
	without := &BusMessage{ID: "att00002", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "user", ToAssignee: "user", Body: "plain"}
	for _, m := range []*BusMessage{with, without} {
		if err := InsertBusMessage(db, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := GetBusMessageByID(db, "att00001")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if len(got.Attachments) != 2 || got.Attachments[0] != with.Attachments[0] || got.Attachments[1] != with.Attachments[1] {
		t.Errorf("attachments = %+v", got.Attachments)
	}
	plain, _ := GetBusMessageByID(db, "att00002")
	if plain.Attachments != nil {
		t.Errorf("plain row attachments = %+v", plain.Attachments)
	}
	var raw *string
	if err := db.QueryRow(`SELECT attachments FROM bus_messages WHERE id='att00002'`).Scan(&raw); err != nil || raw != nil {
		t.Errorf("plain row column = %v %v, want NULL", raw, err)
	}
}

// The column is added to a database created before attachments existed;
// its rows keep working.
func TestBusAttachmentsMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE bus_messages DROP COLUMN attachments`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bus_messages (id, created_at, kind, from_assignee, to_assignee, body)
		VALUES ('old00001', ?, 'message', 'user', 'user', 'old row')`, NowISO()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = OpenDB(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if has, _ := columnExists(db, "bus_messages", "attachments"); !has {
		t.Fatal("attachments column not migrated")
	}
	if m, err := GetBusMessageByID(db, "old00001"); err != nil || m == nil || m.Body != "old row" || m.Attachments != nil {
		t.Errorf("old row = %+v %v", m, err)
	}
}
