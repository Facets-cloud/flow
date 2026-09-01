package app

import (
	"fmt"
	"os"
	"strings"

	"flow/internal/flowdb"
)

// cmdWatch manages broadcast subscriptions:
//
//	flow watch <task-slug|project-slug|assignee>   subscribe
//	flow watch --list                              your subscriptions
//	flow watch --rm <target>                       unsubscribe
//	flow watch <target> --as self                  subscribe as the human even
//	                                               from inside a bound session
//
// The subscriber is the current identity: a bound session watches as
// "self/<its-task-slug>"; an unbound/human invocation (or --as self) watches
// as "self". Posts fan out only to watchers subscribed at post time.
// Consume the resulting messages with `flow inbox` / `flow inbox pop`.
func cmdWatch(args []string) int {
	target := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	fs := flagSet("watch")
	list := fs.Bool("list", false, "list this identity's watches")
	rm := fs.String("rm", "", "unsubscribe from a target")
	as := consumerFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	watcher := resolveConsumer(*as).identity()

	switch {
	case *list:
		watches, err := flowdb.ListWatches(db, watcher)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if len(watches) == 0 {
			fmt.Printf("%s watches nothing\n", watcher)
			return 0
		}
		for _, w := range watches {
			fmt.Println(w)
		}
		return 0
	case *rm != "":
		removed, err := flowdb.RemoveWatch(db, watcher, *rm)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if removed {
			fmt.Printf("%s no longer watches %s\n", watcher, *rm)
		} else {
			fmt.Printf("%s was not watching %s\n", watcher, *rm)
		}
		return 0
	}

	if target == "" || len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: flow watch <task-slug|project-slug|assignee> [--as <assignee>] | --list | --rm <target>")
		return 2
	}
	if err := flowdb.AddWatch(db, watcher, target); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s now watches %s — future posts arrive in `flow inbox`\n", watcher, target)
	return 0
}
