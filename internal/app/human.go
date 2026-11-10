package app

import (
	"crypto/subtle"
	"fmt"
	"os"
	"strings"

	"flow/internal/flowdb"
)

// humanReplyMax caps a relayed answer. It is the human's own words, so it
// is allowed to be longer than an agent message (busBodyMax).
const humanReplyMax = 4000

const humanUsage = `usage: flow human <reply|seen> ...   (connectors only — needs FLOW_CONNECTOR_TOKEN)
  flow human reply <id> "<text>"   record the human's answer; delivers it to the asking task (reply_to=<id>),
                                   or back to the peer hub the message came from
  flow human seen <id>             mark human mail seen without answering`

// cmdHuman implements the connector → flow half of the contract. Only a
// process holding an installed connector's secret (FLOW_CONNECTOR_TOKEN,
// handed to it by flow) may mark human mail seen or answer it; agents
// never have that secret, so they cannot answer for the human.
func cmdHuman(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, humanUsage)
		return 2
	}
	switch args[0] {
	case "reply":
		if len(args) != 3 || strings.TrimSpace(args[2]) == "" {
			fmt.Fprintln(os.Stderr, `usage: flow human reply <id> "<text>"`)
			return 2
		}
		if len(args[2]) > humanReplyMax {
			fmt.Fprintf(os.Stderr, "error: reply is %d chars (max %d)\n", len(args[2]), humanReplyMax)
			return 2
		}
		return humanReply(args[1], strings.TrimSpace(args[2]))
	case "seen":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: flow human seen <id>")
			return 2
		}
		return humanSeen(args[1])
	case "-h", "--help", "help":
		fmt.Println(humanUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown human subcommand %q\n%s\n", args[0], humanUsage)
		return 2
	}
}

// callingConnector returns the installed connector whose secret matches
// FLOW_CONNECTOR_TOKEN, or "" when there is none.
func callingConnector() string {
	tok := os.Getenv("FLOW_CONNECTOR_TOKEN")
	if tok == "" {
		return ""
	}
	store, err := secretStore()
	if err != nil {
		return ""
	}
	for _, m := range installedConnectors() {
		secret, err := store.Get(m.Name)
		if err != nil || secret == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(secret), []byte(tok)) == 1 {
			return m.Name
		}
	}
	return ""
}

// humanMailRow opens the bus and loads id, requiring it to be mail to the
// local human. Returns rc != 0 on failure (message already printed).
func humanMailRow(id string) (*flowdb.BusMessage, int) {
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, 1
	}
	defer db.Close()
	m, err := flowdb.GetBusMessageByID(db, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, 1
	}
	if m == nil {
		fmt.Fprintf(os.Stderr, "error: no message %q\n", id)
		return nil, 1
	}
	if m.ToTaskSlug != "" || m.ToAssignee != busUser {
		fmt.Fprintf(os.Stderr, "error: %s is not mail to the human\n", id)
		return nil, 1
	}
	return m, 0
}

func notAConnector() int {
	fmt.Fprintln(os.Stderr, "error: not a connector — flow human reply|seen need FLOW_CONNECTOR_TOKEN from an installed connector")
	return 1
}

func humanSeen(id string) int {
	name := callingConnector()
	if name == "" {
		return notAConnector()
	}
	m, rc := humanMailRow(id)
	if rc != 0 {
		return rc
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	c := flowdb.Consumer{By: "connector", Identity: "connector:" + name}
	if _, err := flowdb.MarkSeen(db, []string{m.ID}, c); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("seen %s\n", m.ID)
	return 0
}

func humanReply(id, text string) int {
	name := callingConnector()
	if name == "" {
		return notAConnector()
	}
	m, rc := humanMailRow(id)
	if rc != 0 {
		return rc
	}
	if m.Kind != "message" {
		fmt.Fprintf(os.Stderr, "error: %s is a broadcast — nothing to answer (use flow human seen)\n", id)
		return 1
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	// Deliver to the asking task only while its session can still read it.
	// An ask from a peer hub is answered back to that peer instead.
	var reply *flowdb.BusMessage
	undeliverable := ""
	if m.IsPeerInbound() {
		if p, err := flowdb.GetPeer(db, m.Peer); err != nil || p == nil {
			undeliverable = fmt.Sprintf("peer %q no longer exists", m.Peer)
		} else {
			reply = &flowdb.BusMessage{
				ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
				FromAssignee: busUser, ToAssignee: p.Name, Body: text, ReplyTo: m.ID,
				Peer: p.Name, Delivery: flowdb.DeliveryDispatched,
			}
		}
	} else if m.FromTaskSlug == "" {
		undeliverable = "the ask came from no task"
	} else if t, err := flowdb.GetTask(db, m.FromTaskSlug); err != nil || t == nil {
		undeliverable = fmt.Sprintf("task %q no longer exists", m.FromTaskSlug)
	} else if t.Status == "done" {
		undeliverable = fmt.Sprintf("task %q is done", t.Slug)
	} else if t.ArchivedAt.Valid {
		undeliverable = fmt.Sprintf("task %q is archived", t.Slug)
	} else {
		reply = &flowdb.BusMessage{
			ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
			FromAssignee: busUser, ToAssignee: m.FromAssignee, ToTaskSlug: m.FromTaskSlug,
			Body: text, ReplyTo: m.ID,
		}
	}
	c := flowdb.Consumer{By: "connector", Identity: "connector:" + name}
	newly, err := flowdb.AnswerHumanMessage(db, m.ID, c, reply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if !newly {
		fmt.Printf("%s was already answered — nothing to do\n", m.ID)
		return 0
	}
	if reply == nil {
		fmt.Fprintf(os.Stderr, "warning: answer to %s recorded but not delivered: %s\n", m.ID, undeliverable)
		return 0
	}
	if reply.IsPeerOutbound() {
		dispatchPeer(db, reply)
		fmt.Printf("answered %s → peer %s [%s]\n", m.ID, reply.Peer, reply.ID)
		return 0
	}
	fmt.Printf("answered %s → %s/%s [%s]\n", m.ID, reply.ToAssignee, reply.ToTaskSlug, reply.ID)
	return 0
}
