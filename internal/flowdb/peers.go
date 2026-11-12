package flowdb

import (
	"database/sql"
	"fmt"
	"strings"
)

// Peer hubs: another flow home in this hub's contact book. A peer is a
// local nickname for an opaque endpoint (`at`) on an installed connector
// with role "peer"; only that connector interprets `at`. Threading across
// hubs is by message id: an inbound row stores the other hub's id in
// origin_ref, and a reply carries it back as reply_to_ref.
//
//	peers                       bus_messages (additive, nullable)
//	  name  PK (nickname)         peer           outbound: target · inbound: source
//	  connector, at  UNIQUE       origin_ref     inbound: the other hub's id
//	  blocked, created_at         delivery       outbound: dispatched|sent|failed
//	  last_seen_at                delivery_error
//	                              UNIQUE(peer, origin_ref) WHERE origin_ref NOT NULL
const peersDDL = `
CREATE TABLE IF NOT EXISTS peers (
    name          TEXT PRIMARY KEY,
    connector     TEXT NOT NULL,
    at            TEXT NOT NULL,
    blocked       INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    last_seen_at  TEXT,
    UNIQUE (connector, at)
);
`

// peerBusIndexDDL runs after the peer columns exist: a duplicate inbound
// delivery of the same (peer, origin_ref) is refused by the database.
const peerBusIndexDDL = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_bus_messages_peer_origin
    ON bus_messages(peer, origin_ref) WHERE origin_ref IS NOT NULL;
`

// peerBusColumns are the nullable TEXT columns migratePeers adds to
// bus_messages. Part of the schema fingerprint: adding one changes it.
var peerBusColumns = []string{"peer", "origin_ref", "delivery", "delivery_error", "attachments"}

// Delivery states of an outbound peer row.
const (
	DeliveryDispatched = "dispatched"
	DeliverySent       = "sent"
	DeliveryFailed     = "failed"
)

// migratePeers creates the peers table and backfills the peer columns on
// bus_messages (nullable, no data) plus their unique index.
func migratePeers(db *sql.DB) error {
	if _, err := db.Exec(peersDDL); err != nil {
		return fmt.Errorf("peers table: %w", err)
	}
	// attachments (JSON array of {name,size,sha256}) arrived with peer
	// desk primitives; additive and nullable like the peer columns.
	for _, col := range peerBusColumns {
		has, err := columnExists(db, "bus_messages", col)
		if err != nil {
			return err
		}
		if !has {
			if _, err := db.Exec(`ALTER TABLE bus_messages ADD COLUMN ` + col + ` TEXT`); err != nil {
				return fmt.Errorf("bus_messages.%s: %w", col, err)
			}
		}
	}
	if _, err := db.Exec(peerBusIndexDDL); err != nil {
		return fmt.Errorf("peer origin index: %w", err)
	}
	return nil
}

// Peer mirrors one peers row.
type Peer struct {
	Name       string
	Connector  string
	At         string
	Blocked    bool
	CreatedAt  string
	LastSeenAt string
}

const peerCols = `name, connector, at, blocked, created_at, COALESCE(last_seen_at,'')`

func queryPeers(db *sql.DB, where string, args ...any) ([]*Peer, error) {
	rows, err := db.Query(`SELECT `+peerCols+` FROM peers WHERE `+where+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Peer
	for rows.Next() {
		p := &Peer{}
		var blocked int
		if err := rows.Scan(&p.Name, &p.Connector, &p.At, &blocked, &p.CreatedAt, &p.LastSeenAt); err != nil {
			return nil, err
		}
		p.Blocked = blocked == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

func onePeer(db *sql.DB, where string, args ...any) (*Peer, error) {
	ps, err := queryPeers(db, where, args...)
	if err != nil || len(ps) == 0 {
		return nil, err
	}
	return ps[0], nil
}

// AddPeer inserts a peer (created_at stamped now when empty).
func AddPeer(db *sql.DB, p *Peer) error {
	if p.CreatedAt == "" {
		p.CreatedAt = NowISO()
	}
	_, err := db.Exec(`INSERT INTO peers (name, connector, at, blocked, created_at) VALUES (?,?,?,?,?)`,
		p.Name, p.Connector, p.At, boolInt(p.Blocked), p.CreatedAt)
	return err
}

// GetPeer returns a peer by nickname, nil when none.
func GetPeer(db *sql.DB, name string) (*Peer, error) {
	return onePeer(db, `name=?`, name)
}

// GetPeerByEndpoint returns the peer at (connector, at), nil when none.
func GetPeerByEndpoint(db *sql.DB, connector, at string) (*Peer, error) {
	return onePeer(db, `connector=? AND at=?`, connector, at)
}

// ListPeers returns every peer sorted by name.
func ListPeers(db *sql.DB) ([]*Peer, error) {
	return queryPeers(db, `1=1`)
}

// DeletePeer removes a peer; false when there was none.
func DeletePeer(db *sql.DB, name string) (bool, error) {
	res, err := db.Exec(`DELETE FROM peers WHERE name=?`, name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetPeerBlocked blocks or unblocks a peer; false when there was none.
func SetPeerBlocked(db *sql.DB, name string, blocked bool) (bool, error) {
	res, err := db.Exec(`UPDATE peers SET blocked=? WHERE name=?`, boolInt(blocked), name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TouchPeerSeen stamps last_seen_at (an accepted inbound delivery).
func TouchPeerSeen(db *sql.DB, name string) error {
	_, err := db.Exec(`UPDATE peers SET last_seen_at=? WHERE name=?`, NowISO(), name)
	return err
}

// SetDelivery records an outbound peer row's delivery state; errMsg is
// stored for failed and cleared otherwise. A sent row is also acked (by
// "peer"): it left this hub, so it rolls off with normal retention; a
// failed row stays pending — an outstanding, visible send.
func SetDelivery(db *sql.DB, id, state, errMsg string) error {
	if state == DeliverySent {
		_, err := db.Exec(`UPDATE bus_messages SET delivery=?, delivery_error=NULL,
                status='acked', acked_at=COALESCE(acked_at, ?), acked_by=COALESCE(acked_by, 'peer')
            WHERE id=?`, state, NowISO(), id)
		return err
	}
	_, err := db.Exec(`UPDATE bus_messages SET delivery=?, delivery_error=? WHERE id=?`,
		state, NullIfEmpty(errMsg), id)
	return err
}

// RecentPeerMessages returns the newest n rows to or from peer, newest
// first.
func RecentPeerMessages(db *sql.DB, peer string, n int) ([]*BusMessage, error) {
	rows, err := queryBusMsgs(db, `peer=?`, peer)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if len(rows) > n {
		rows = rows[:n]
	}
	return rows, nil
}

// InboundByRef returns the inbound row peer delivered with the other
// hub's id ref, nil when none.
func InboundByRef(db *sql.DB, peer, ref string) (*BusMessage, error) {
	rows, err := queryBusMsgs(db, `peer=? AND origin_ref=?`, peer, ref)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// IsUniqueViolation reports whether err is a SQLite UNIQUE/PRIMARY KEY
// constraint failure.
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
