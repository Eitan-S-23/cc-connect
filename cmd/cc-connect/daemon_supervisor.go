package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/chenhg5/cc-connect/daemon"
)

func runDaemonSupervisor(args []string) {
	fs := flag.NewFlagSet("_daemon-supervise", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "path to config.toml")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *configPath == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Error: _daemon-supervise requires exactly --config PATH")
		os.Exit(2)
	}
	binaryPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: resolve daemon supervisor executable: %v\n", err)
		os.Exit(1)
	}
	if realPath, err := filepath.EvalSymlinks(binaryPath); err == nil {
		binaryPath = realPath
	}
	logMaxSize := int64(daemon.DefaultLogMaxSize)
	if value := os.Getenv("CC_LOG_MAX_SIZE"); value != "" {
		if parsed, err := daemon.ParseLogSize(value); err == nil && parsed > 0 {
			logMaxSize = parsed
		}
	}
	logBackups := daemon.DefaultLogMaxBackups
	if value := os.Getenv("CC_LOG_MAX_BACKUPS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			logBackups = parsed
		}
	}
	if err := daemon.RunSupervisor(daemon.SupervisorConfig{
		BinaryPath: binaryPath,
		ConfigPath: *configPath,
		WorkDir:    filepath.Dir(*configPath),
		LogFile:    os.Getenv("CC_LOG_FILE"),
		LogMaxSize: logMaxSize,
		LogBackups: logBackups,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Daemon supervisor failed: %v\n", err)
		os.Exit(1)
	}
}
