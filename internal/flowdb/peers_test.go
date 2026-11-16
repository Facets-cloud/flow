package flowdb

import (
	"path/filepath"
	"testing"
)

// Peer hubs v0 — schema: the peers table and the additive bus columns.

func TestPeersCRUD(t *testing.T) {
	db := openBusTestDB(t)

	if err := AddPeer(db, &Peer{Name: "bob", Connector: "peer-local", At: "/homes/b"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	p, err := GetPeer(db, "bob")
	if err != nil || p == nil {
		t.Fatalf("get: %v %v", p, err)
	}
	if p.Connector != "peer-local" || p.At != "/homes/b" || p.Blocked || p.CreatedAt == "" || p.LastSeenAt != "" {
		t.Errorf("peer = %+v", p)
	}
	// Name is the primary key; (connector, at) is unique.
	if err := AddPeer(db, &Peer{Name: "bob", Connector: "peer-local", At: "/homes/x"}); err == nil {
		t.Errorf("duplicate name accepted")
	}
	if err := AddPeer(db, &Peer{Name: "robert", Connector: "peer-local", At: "/homes/b"}); err == nil {
		t.Errorf("duplicate (connector, at) accepted")
	}
	if err := AddPeer(db, &Peer{Name: "robert", Connector: "other", At: "/homes/b"}); err != nil {
		t.Errorf("same at on another connector rejected: %v", err)
	}

	byAt, err := GetPeerByEndpoint(db, "peer-local", "/homes/b")
	if err != nil || byAt == nil || byAt.Name != "bob" {
		t.Errorf("by endpoint = %+v %v", byAt, err)
	}
	if none, err := GetPeerByEndpoint(db, "peer-local", "/nowhere"); err != nil || none != nil {
		t.Errorf("unknown endpoint = %+v %v", none, err)
	}
	if none, err := GetPeer(db, "nobody"); err != nil || none != nil {
		t.Errorf("unknown peer = %+v %v", none, err)
	}

	if ok, err := SetPeerBlocked(db, "bob", true); err != nil || !ok {
		t.Fatalf("block: %v %v", ok, err)
	}
	if p, _ := GetPeer(db, "bob"); !p.Blocked {
		t.Errorf("not blocked")
	}
	if ok, _ := SetPeerBlocked(db, "nobody", true); ok {
		t.Errorf("blocked a missing peer")
	}
	if err := TouchPeerSeen(db, "bob"); err != nil {
		t.Fatal(err)
	}
	if p, _ := GetPeer(db, "bob"); p.LastSeenAt == "" {
		t.Errorf("last_seen_at not stamped")
	}

	all, err := ListPeers(db)
	if err != nil || len(all) != 2 || all[0].Name != "bob" || all[1].Name != "robert" {
		t.Errorf("list = %+v %v", all, err)
	}
	if ok, err := DeletePeer(db, "robert"); err != nil || !ok {
		t.Errorf("delete: %v %v", ok, err)
	}
	if ok, _ := DeletePeer(db, "robert"); ok {
		t.Errorf("deleted twice")
	}
}

func TestBusPeerColumnsRoundtrip(t *testing.T) {
	db := openBusTestDB(t)
	out := &BusMessage{ID: "out1", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "user", FromTaskSlug: "t", ToAssignee: "bob", Body: "ship?",
		Peer: "bob", Delivery: "dispatched"}
	if err := InsertBusMessage(db, out); err != nil {
		t.Fatal(err)
	}
	if out.Ping {
		t.Errorf("outbound peer row must never ping the local human")
	}
	if err := SetDelivery(db, "out1", "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	got, _ := GetBusMessageByID(db, "out1")
	if got.Peer != "bob" || got.Delivery != "failed" || got.DeliveryError != "boom" || got.OriginRef != "" {
		t.Errorf("outbound = %+v", got)
	}
	if err := SetDelivery(db, "out1", "sent", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetBusMessageByID(db, "out1"); got.Delivery != "sent" || got.DeliveryError != "" {
		t.Errorf("sent = %+v", got)
	}

	in := &BusMessage{ID: "in1", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "bob", ToAssignee: "user", Body: "hello", Peer: "bob", OriginRef: "theirs1"}
	if err := InsertBusMessage(db, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetBusMessageByID(db, "in1"); got.OriginRef != "theirs1" || got.Peer != "bob" {
		t.Errorf("inbound = %+v", got)
	}
	// Duplicate delivery of the same (peer, origin_ref) is refused.
	dup := &BusMessage{ID: "in2", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "bob", ToAssignee: "user", Body: "hello", Peer: "bob", OriginRef: "theirs1"}
	if err := InsertBusMessage(db, dup); err == nil || !IsUniqueViolation(err) {
		t.Errorf("duplicate origin_ref insert err = %v, want unique violation", err)
	}
	// Another peer may reuse the same ref; NULL origin_refs never collide.
	other := &BusMessage{ID: "in3", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "carol", ToAssignee: "user", Body: "x", Peer: "carol", OriginRef: "theirs1"}
	if err := InsertBusMessage(db, other); err != nil {
		t.Errorf("other peer same ref: %v", err)
	}
	if err := InsertBusMessage(db, &BusMessage{ID: "out2", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "user", ToAssignee: "bob", Body: "y", Peer: "bob"}); err != nil {
		t.Errorf("second outbound (NULL origin_ref): %v", err)
	}
	if m, err := InboundByRef(db, "bob", "theirs1"); err != nil || m == nil || m.ID != "in1" {
		t.Errorf("InboundByRef = %+v %v", m, err)
	}
}

// Columns and the unique index are added to a database created before
// peers existed.
func TestBusPeerColumnsMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_bus_messages_peer_origin`,
		`ALTER TABLE bus_messages DROP COLUMN peer`,
		`ALTER TABLE bus_messages DROP COLUMN origin_ref`,
		`ALTER TABLE bus_messages DROP COLUMN delivery`,
		`ALTER TABLE bus_messages DROP COLUMN delivery_error`,
		`DROP TABLE peers`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	markPreFingerprint(t, db)
	db.Close()
	db, err = OpenDB(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	for _, c := range []string{"peer", "origin_ref", "delivery", "delivery_error"} {
		if has, _ := columnExists(db, "bus_messages", c); !has {
			t.Errorf("column %s not migrated", c)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_bus_messages_peer_origin'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("unique index missing: %d %v", n, err)
	}
}
