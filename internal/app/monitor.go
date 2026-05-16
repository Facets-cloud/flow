package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"flow/internal/flowdb"
	"flow/internal/monitor"
	"fmt"
	"os"
	"time"
)

func cmdMonitor(args []string) int {
	if len(args) == 0 {
		return cmdMonitorPoll(nil)
	}
	switch args[0] {
	case "poll":
		return cmdMonitorPoll(args[1:])
	case "list":
		return cmdMonitorList(args[1:])
	case "rules":
		return cmdMonitorRules(args[1:])
	case "-h", "--help", "help":
		printMonitorUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown monitor subcommand %q\n", args[0])
		printMonitorUsage()
		return 2
	}
}

func printMonitorUsage() {
	fmt.Println(`flow monitor

Usage:
  flow monitor poll [--source all|github|slack] [--once] [--interval 60s] [--json]
  flow monitor list [--json]
  flow monitor rules [--set source.kind=mode]

Poll GitHub through gh and Slack through the Slack Web API when
FLOW_SLACK_TOKEN, SLACK_USER_TOKEN, SLACK_BOT_TOKEN, or SLACK_TOKEN is set.
FLOW_SLACK_POLL_CMD can override Slack polling with a custom JSON command.
Slack token polling reads auth.test, users.conversations,
conversations.history, and chat.getPermalink.
Events are stored in flow.db and feed the UI notification/autonomy layer.`)
}

func cmdMonitorPoll(args []string) int {
	fs := flagSet("monitor poll")
	source := fs.String("source", "all", "source to poll: all, github, or slack")
	once := fs.Bool("once", true, "poll once and exit")
	interval := fs.Duration("interval", time.Minute, "daemon poll interval when --once=false")
	jsonOut := fs.Bool("json", false, "print JSON summary")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	db, err := openMonitorDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	poller := monitor.Poller{DB: db}
	run := func() []monitor.PollSummary {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		summaries, err := poller.Poll(ctx, *source)
		if err != nil {
			summaries = []monitor.PollSummary{{Source: *source, Errors: []string{err.Error()}, LastSync: flowdb.NowISO()}}
		}
		if *jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(summaries)
		} else {
			for _, s := range summaries {
				if len(s.Errors) > 0 {
					fmt.Printf("%s: %d events (%d new), errors: %v\n", s.Source, s.Events, s.New, s.Errors)
				} else {
					fmt.Printf("%s: %d events (%d new)\n", s.Source, s.Events, s.New)
				}
			}
		}
		return summaries
	}
	run()
	if *once {
		return 0
	}
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for range tick.C {
		run()
	}
	return 0
}

func cmdMonitorList(args []string) int {
	fs := flagSet("monitor list")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	db, err := openMonitorDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	events, err := flowdb.ListMonitorEvents(db, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(events)
		return 0
	}
	if len(events) == 0 {
		fmt.Println("(no monitor events)")
		return 0
	}
	for _, e := range events {
		fmt.Printf("%s %-8s %-18s %-10s %s\n", e.LastSeenAt, e.Source, e.Kind, e.Status, e.Title)
	}
	return 0
}

func cmdMonitorRules(args []string) int {
	fs := flagSet("monitor rules")
	set := fs.String("set", "", "set mode as source.kind=mode")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	db, err := openMonitorDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	if *set != "" {
		source, kind, mode, ok := parseRuleSet(*set)
		if !ok {
			fmt.Fprintln(os.Stderr, "error: --set must look like source.kind=mode")
			return 2
		}
		if err := flowdb.SetAutomationRuleMode(db, source, kind, mode); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}
	rules, err := flowdb.ListAutomationRules(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, r := range rules {
		fmt.Printf("%s.%s=%s\n", r.Source, r.Kind, r.Mode)
	}
	return 0
}

func parseRuleSet(raw string) (string, string, string, bool) {
	left, mode, ok := stringsCut(raw, "=")
	if !ok {
		return "", "", "", false
	}
	source, kind, ok := stringsCut(left, ".")
	if !ok || source == "" || kind == "" || mode == "" {
		return "", "", "", false
	}
	return source, kind, mode, true
}

func stringsCut(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

func openMonitorDB() (*sql.DB, error) {
	dbPath, err := flowDBPath()
	if err != nil {
		return nil, err
	}
	return flowdb.OpenDB(dbPath)
}
