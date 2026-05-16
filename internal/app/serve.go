package app

import (
	"errors"
	"flag"
	"flow/internal/agenthooks"
	"flow/internal/flowdb"
	"flow/internal/server"
	"flow/internal/workdirreg"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func cmdUI(args []string) int {
	if len(args) == 0 {
		printUIUsage()
		return 0
	}
	switch args[0] {
	case "serve":
		return cmdUIServe(args[1:])
	case "-h", "--help", "help":
		printUIUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown ui subcommand %q\n", args[0])
		printUIUsage()
		return 2
	}
}

func printUIUsage() {
	fmt.Println(`flow ui

Usage:
  flow ui serve [--host 127.0.0.1] [--port 8787] [--bg]

Serve the local Mission Control UI backed by the current flow database.`)
}

func cmdUIServe(args []string) int {
	fs := flagSet("ui serve")
	host := fs.String("host", "127.0.0.1", "host to bind")
	port := fs.Int("port", 8787, "TCP port to bind")
	bg := fs.Bool("bg", false, "run the UI server in the background")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "error: ui serve takes no positional arguments")
		return 2
	}
	if *bg {
		return startUIBackground(*host, *port)
	}
	return serveUI(*host, *port)
}

func serveUI(host string, port int) int {
	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return 1
	}
	defer db.Close()
	if _, err := workdirreg.SyncGitRemotes(db); err != nil {
		fmt.Fprintf(os.Stderr, "warning: sync workdir remotes: %v\n", err)
	}
	if changed, err := agenthooks.InstallKnownWorkdirs(db); err != nil {
		fmt.Fprintf(os.Stderr, "warning: install local agent hooks for existing workdirs: %v\n", err)
	} else if changed > 0 {
		fmt.Fprintf(os.Stderr, "installed local agent hooks in %d existing workdir(s)\n", changed)
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: find executable: %v\n", err)
		return 1
	}
	commandPath := preferredUIFlowBinary(exe)
	hookHost := host
	if hookHost == "0.0.0.0" || hookHost == "::" {
		hookHost = "127.0.0.1"
	}
	srv := server.New(server.Config{
		DB:          db,
		FlowRoot:    root,
		Version:     Version,
		CommandPath: commandPath,
		HookURL:     "http://" + net.JoinHostPort(hookHost, strconv.Itoa(port)) + "/api/hooks/agent",
	})
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	fmt.Fprintf(os.Stderr, "flow ui listening on http://%s\n", addr)
	if commandPath != exe {
		fmt.Fprintf(os.Stderr, "flow ui commands using %s\n", commandPath)
	}
	if host == "0.0.0.0" {
		fmt.Fprintln(os.Stderr, "warning: bound to 0.0.0.0; anyone on this network can read and operate your flow data")
	}
	return srv.ListenAndServe(addr)
}

func startUIBackground(host string, port int) int {
	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: find executable: %v\n", err)
		return 1
	}
	exe = preferredUIFlowBinary(exe)
	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: create log dir: %v\n", err)
		return 1
	}
	logPath := filepath.Join(logDir, "ui-serve.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open log: %v\n", err)
		return 1
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "ui", "serve", "--host", host, "--port", strconv.Itoa(port))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "error: start background server: %v\n", err)
		return 1
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not release background process: %v\n", err)
	}
	fmt.Printf("flow ui serving at http://%s\n", net.JoinHostPort(host, strconv.Itoa(port)))
	fmt.Printf("pid %d · log %s\n", pid, logPath)
	return 0
}

func preferredUIFlowBinary(fallback string) string {
	if override := strings.TrimSpace(os.Getenv("FLOW_UI_FLOW_BIN")); override != "" {
		if isExecutableFile(override) {
			return override
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if found := findNearestBinFlow(cwd); found != "" {
			return found
		}
	}
	if fallback != "" {
		if found := findNearestBinFlow(filepath.Dir(fallback)); found != "" {
			return found
		}
	}
	return fallback
}

func findNearestBinFlow(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, "bin", "flow")
		if isExecutableFile(candidate) {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}
