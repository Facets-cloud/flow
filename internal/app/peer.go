package app

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"

	"flow/internal/connector"
	"flow/internal/flowdb"
)

// Peer hubs: other flow homes in this hub's contact book, reached through
// an installed connector with role "peer". A peer is a local nickname for
// an opaque endpoint (`at`) that only that connector interprets. See the
// skill reference references/peers.md.
//
//	hub A                                   hub B
//	flow message bob "ship?" ──► row(peer=bob, delivery=dispatched)
//	   └ detached dispatch ──► A's peer connector `run send`
//	                              └──► B: flow peer deliver --at <A> --ref <idA>
//	                                         (B's connector token) ──► B's user mail
//	B: flow human reply <idB> "Yes" ──► row(peer=alice) ──► … ──►
//	   A: flow peer deliver --ref <idB> --reply-to-ref <idA> ──► A's user/<task>

const peerUsage = `usage: flow peer <add|list|show|rm|block|unblock> ...
  flow peer add <name> --via <connector> --at <endpoint>
                                  add another flow hub to your contact book (the
                                  connector must be installed with role "peer";
                                  <endpoint> is opaque — only the connector reads it)
  flow peer list                  your peers
  flow peer show <name>           one peer's details
  flow peer rm <name>             forget a peer
  flow peer block|unblock <name>  refuse / accept its inbound messages
  (message a peer: flow message <name> "<body>" — never <name>/<task>)`

var peerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// cmdPeer implements `flow peer ...`.
func cmdPeer(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, peerUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return cmdPeerAdd(args[1:])
	case "list", "ls":
		return cmdPeerList(args[1:])
	case "show":
		return cmdPeerShow(args[1:])
	case "rm", "remove":
		return cmdPeerRemove(args[1:])
	case "block":
		return cmdPeerBlock(args[1:], true)
	case "unblock":
		return cmdPeerBlock(args[1:], false)
	case "deliver":
		return cmdPeerDeliver(args[1:])
	case "-h", "--help", "help":
		fmt.Println(peerUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown peer subcommand %q\n%s\n", args[0], peerUsage)
		return 2
	}
}

// validPeerName rejects names that could collide with local addresses.
func validPeerName(name string) error {
	if isReservedBusName(name) {
		return fmt.Errorf("%q is reserved for the local human", name)
	}
	if !peerNameRe.MatchString(name) {
		return fmt.Errorf("peer name %q must be letters, digits, '.', '-' or '_' (no '/')", name)
	}
	return nil
}

// peerConnector returns the installed connector name with role "peer",
// or an error saying why it is not usable.
func peerConnector(name string) (*connector.Manifest, error) {
	for _, m := range installedConnectors() {
		if m.Name != name {
			continue
		}
		if !m.HasRole(connector.RolePeer) {
			return nil, fmt.Errorf("connector %q has no role \"peer\" (roles: %v) — it cannot reach other hubs", name, m.EffectiveRoles())
		}
		return m, nil
	}
	return nil, fmt.Errorf("no connector %q installed (add one with role \"peer\": flow connector add <dir|sample>)", name)
}

func cmdPeerAdd(args []string) int {
	const usage = "usage: flow peer add <name> --via <connector> --at <endpoint>"
	var name, via, at string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--via", "--at":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "error: %s needs a value\n%s\n", a, usage)
				return 2
			}
			if a == "--via" {
				via = args[i+1]
			} else {
				at = args[i+1]
			}
			i++
		default:
			if name != "" || (len(a) > 0 && a[0] == '-') {
				fmt.Fprintf(os.Stderr, "error: unexpected argument %q\n%s\n", a, usage)
				return 2
			}
			name = a
		}
	}
	if name == "" || via == "" || at == "" {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if err := validPeerName(name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	if _, err := peerConnector(via); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	if p, _ := flowdb.GetPeer(db, name); p != nil {
		fmt.Fprintf(os.Stderr, "error: peer %q already exists (flow peer rm %s first)\n", name, name)
		return 1
	}
	if p, _ := flowdb.GetPeerByEndpoint(db, via, at); p != nil {
		fmt.Fprintf(os.Stderr, "error: %s endpoint %q is already peer %q\n", via, at, p.Name)
		return 1
	}
	if err := flowdb.AddPeer(db, &flowdb.Peer{Name: name, Connector: via, At: at}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("added peer %s via %s at %s\n", name, via, at)
	return 0
}

func cmdPeerList(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: flow peer list")
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	ps, err := flowdb.ListPeers(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(ps) == 0 {
		fmt.Println("no peers — add another flow hub: flow peer add <name> --via <connector> --at <endpoint>")
		return 0
	}
	for _, p := range ps {
		state := ""
		if p.Blocked {
			state = "  BLOCKED"
		}
		seen := p.LastSeenAt
		if seen == "" {
			seen = "never"
		}
		fmt.Printf("%-16s via %-12s at %s  last seen %s%s\n", p.Name, p.Connector, p.At, seen, state)
	}
	return 0
}

// peerArg opens the bus and loads the named peer (rc != 0 on failure,
// message already printed). The caller closes db when rc == 0.
func peerArg(args []string, verb string) (*sql.DB, *flowdb.Peer, int) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: flow peer %s <name>\n", verb)
		return nil, nil, 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, nil, 1
	}
	p, err := flowdb.GetPeer(db, args[0])
	if err != nil {
		db.Close()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, nil, 1
	}
	if p == nil {
		db.Close()
		fmt.Fprintf(os.Stderr, "error: no peer %q (flow peer list)\n", args[0])
		return nil, nil, 1
	}
	return db, p, 0
}

func cmdPeerShow(args []string) int {
	db, p, rc := peerArg(args, "show")
	if rc != 0 {
		return rc
	}
	defer db.Close()
	yn := map[bool]string{true: "yes", false: "no"}
	seen := p.LastSeenAt
	if seen == "" {
		seen = "never"
	}
	fmt.Printf("peer:       %s\n", p.Name)
	fmt.Printf("via:        %s\n", p.Connector)
	fmt.Printf("at:         %s\n", p.At)
	fmt.Printf("blocked:    %s\n", yn[p.Blocked])
	fmt.Printf("added:      %s\n", p.CreatedAt)
	fmt.Printf("last seen:  %s\n", seen)
	if _, err := peerConnector(p.Connector); err != nil {
		fmt.Printf("warning:    %v\n", err)
	}
	return 0
}

func cmdPeerRemove(args []string) int {
	db, p, rc := peerArg(args, "rm")
	if rc != 0 {
		return rc
	}
	defer db.Close()
	if _, err := flowdb.DeletePeer(db, p.Name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("removed peer %s\n", p.Name)
	return 0
}

func cmdPeerBlock(args []string, block bool) int {
	verb := map[bool]string{true: "block", false: "unblock"}[block]
	db, p, rc := peerArg(args, verb)
	if rc != 0 {
		return rc
	}
	defer db.Close()
	if _, err := flowdb.SetPeerBlocked(db, p.Name, block); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if block {
		fmt.Printf("blocked peer %s — its inbound messages are rejected\n", p.Name)
	} else {
		fmt.Printf("unblocked peer %s\n", p.Name)
	}
	return 0
}

// cmdPeerDeliver is the connector-only inbound door (next behavior).
func cmdPeerDeliver(args []string) int {
	fmt.Fprintln(os.Stderr, "error: flow peer deliver is not implemented yet")
	return 1
}
