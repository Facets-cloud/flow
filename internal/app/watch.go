package app

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"flow/internal/flowdb"
)

// cmdWatch manages broadcast subscriptions:
//
//   flow watch <task-slug|project-slug|assignee>   subscribe
//   flow watch --list                              your subscriptions
//   flow watch --rm <target>                       unsubscribe
//   flow watch --follow                            live feed (human)
//
// The subscriber is the current identity: a bound session watches as
// "self/<its-task-slug>"; an unbound/human invocation watches as
// "self". Posts fan out only to watchers subscribed at post time.
func cmdWatch(args []string) int {
	fs := flagSet("watch")
	list := fs.Bool("list", false, "list this identity's watches")
	rm := fs.String("rm", "", "unsubscribe from a target")
	follow := fs.Bool("follow", false, "stream your pages + posts live (human feed)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openPagerDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	s := currentSender()
	watcher := pagerSelf
	if s.TaskSlug != "" {
		watcher = pagerSelf + "/" + s.TaskSlug
	}

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
	case *follow:
		return watchFollow(db)
	}

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: flow watch <task-slug|project-slug|assignee> | --list | --rm <target> | --follow")
		return 2
	}
	target := rest[0]
	if err := flowdb.AddWatch(db, watcher, target); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s now watches %s — future posts arrive as messages\n", watcher, target)
	return 0
}

// watchFollow streams the human's pages + posts until interrupted.
func watchFollow(pdb *sql.DB) int {
	fmt.Println("following your feed (ctrl-c to stop)")
	for {
		pages, _ := flowdb.PendingHumanPages(pdb, pagerSelf)
		posts, _ := flowdb.PendingPostsForHuman(pdb, pagerSelf)
		rows := append(pages, posts...)
		if len(rows) > 0 {
			printPageRows(rows)
			var ids []string
			for _, m := range rows {
				if m.Kind == "page" {
					_, _ = flowdb.AckPageByID(pdb, m.ID, "watch-follow")
				} else {
					ids = append(ids, m.ID)
				}
			}
			_ = flowdb.MarkDelivered(pdb, ids)
		}
		time.Sleep(2 * time.Second)
	}
}
