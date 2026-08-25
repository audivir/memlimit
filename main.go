package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

var version = "dev"

const (
	checkInterval  = 100 * time.Millisecond // safety check interval (fast)
	printInterval  = 5 * time.Second        // UI update interval (slow)
	changeLimit    = 1 * 1024 * 1024        // update UI if change > 1MB
	defaultTimeout = 1 * time.Second        // default timeout for graceful shutdown
)

// printVersion prints the version of the program
func printVersion() {
	fmt.Printf("%s version %s\n", path.Base(os.Args[0]), version)
}

// printHelp prints the complete help message
func printHelp() {
	const helpText = `%s %s

Watches the memory usage of a process and its children.
If the total memory usage (evaluated as RSS) exceeds a specified limit,
the process tree is terminated (SIGTERM then SIGKILL).

Usage:
  %s <PID> <LIMIT> [TIMEOUT]

Arguments:
  PID      The Process ID (integer) to monitor.
  LIMIT    Memory limit with unit (K, M, G, T).
           Optional B suffix. Case insensitive.
  TIMEOUT  (Optional) Duration to wait for graceful shutdown (SIGTERM)
           before force killing (SIGKILL). Default: %s.
           Format examples: 5s, 1m, 500ms. Set to 0s for immediate kill.

Examples:
  %s 12345 500MB   (Gracefully stop for default period if > 500MB)
  %s 9999 2g 10s   (Try graceful stop for 10s, then kill)
  %s 8888 100M 0s  (Kill immediately, no grace period)
`
	// basename of os.Args[0]
	base := path.Base(os.Args[0])
	fmt.Printf(helpText, base, version, base, defaultTimeout, base, base, base)
}

// parseMemoryLimit parses a memory limit string (e.g. "500MB") into bytes.
func parseMemoryLimit(memStr string) (uint64, error) {
	re := regexp.MustCompile(`(?i)^(\d+)([kKmMGTt][bB]?)$`)
	matches := re.FindStringSubmatch(strings.TrimSpace(memStr))

	if matches == nil {
		return 0, errors.New("format must be like '500MB', '2G', '1024KB'")
	}

	value, _ := strconv.ParseUint(matches[1], 10, 64)
	unit := strings.ToUpper(matches[2])

	var multiplier uint64
	switch unit {
	case "K", "KB":
		multiplier = 1024
	case "M", "MB":
		multiplier = 1024 * 1024
	case "G", "GB":
		multiplier = 1024 * 1024 * 1024
	case "T", "TB":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unsupported unit: %s", unit)
	}

	return value * multiplier, nil
}

// formatBytes formats bytes as human-readable string up to exabytes
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// getRecursiveChildren gets all children of a process recursively
func getRecursiveChildren(p *process.Process) ([]*process.Process, error) {
	children, err := p.Children()
	if err != nil {
		return nil, err
	}

	var allChildren []*process.Process
	for _, child := range children {
		allChildren = append(allChildren, child)
		grandChildren, err := getRecursiveChildren(child)
		if err == nil {
			allChildren = append(allChildren, grandChildren...)
		}
	}
	return allChildren, nil
}

// getTotalMemoryUsage gets the total memory usage of a process and its children
func getTotalMemoryUsage(pid int32) (uint64, error) {
	parent, err := process.NewProcess(pid)
	if err != nil {
		return 0, err
	}

	procs := []*process.Process{parent}
	children, _ := getRecursiveChildren(parent)
	procs = append(procs, children...)

	var totalRSS uint64
	for _, p := range procs {
		if isRunning, _ := p.IsRunning(); isRunning {
			if memInfo, err := p.MemoryInfo(); err == nil {
				totalRSS += memInfo.RSS
			}
		}
	}
	return totalRSS, nil
}

// killTree kills a process and its children
func killTree(pid int32, timeout time.Duration) {
	parent, err := process.NewProcess(pid)
	if err != nil {
		return
	}

	// gather all processes in the tree
	procs := []*process.Process{}
	children, _ := getRecursiveChildren(parent)
	procs = append(procs, children...)
	procs = append(procs, parent) // Add parent last

	// graceful shutdown (SIGTERM)
	if timeout > 0 {
		fmt.Printf(">> Attempting graceful shutdown (Timeout: %v)...\n", timeout)
		for _, p := range procs {
			_ = p.Terminate() // SIGTERM
		}

		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {

			allDead := true
			for _, p := range procs {
				// check if it exists in process table
				if run, _ := p.IsRunning(); !run {
					continue
				}

				// check for zombie processes waiting to be reaped (Z)
				// no memory and status "Z" or "z" or "zombie"
				status, err := p.Status()
				if err == nil && len(status) > 0 {
					// Linux/Unix returns "Z" or "T" (stopped)
					if status[0] == "Z" || status[0] == "z" || status[0] == "zombie" {
						continue
					}
				}

				allDead = false
				break
			}

			if allDead {
				fmt.Println(">> All processes stopped gracefully.")
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Println(">> Timeout reached. Force killing...")
	}

	// force kill (SIGKILL)
	for _, p := range procs {
		_ = p.Kill()
	}
	fmt.Println(">> Process tree force killed.")
}

// watchAndKill watches a process and its children.
// If the memory usage exceeds the limit, it kills the process tree.
func watchAndKill(pid int32, limitBytes uint64, timeout time.Duration) {
	fmt.Printf(">> Monitoring PID %d | Limit: %s", pid, formatBytes(limitBytes))
	if timeout > 0 {
		fmt.Printf(" | Graceful Timeout: %v", timeout)
	}
	fmt.Println("\n>> Press Ctrl+C to stop monitoring.")

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	var lastPrintedUsage uint64
	lastPrintTime := time.Now()

	usagePrinted := false

	for range ticker.C {
		exists, _ := process.PidExists(pid)
		if !exists {
			fmt.Println(">> Process ended naturally.")
			return
		}

		memUsage, err := getTotalMemoryUsage(pid)
		if err != nil {
			continue
		}

		// LIMIT EXCEEDED
		if memUsage > limitBytes {
			if usagePrinted {
				fmt.Println()
			}
			fmt.Printf("!!! LIMIT EXCEEDED (%s) !!!\n", formatBytes(memUsage))
			killTree(pid, timeout)
			return
		}

		// Update UI
		diff := int64(memUsage) - int64(lastPrintedUsage)
		if diff < 0 {
			diff = -diff
		}

		if time.Since(lastPrintTime) > printInterval || diff > changeLimit {
			fmt.Printf("\r>> Current Usage: %-10s", formatBytes(memUsage))
			lastPrintedUsage = memUsage
			lastPrintTime = time.Now()
			usagePrinted = true
		}
	}
}

func main() {
	args := os.Args[1:]

	// help check
	if len(args) == 0 {
		printHelp()
		os.Exit(1)
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" || arg == "help" {
			printHelp()
			os.Exit(0)
		}
	}

	for _, arg := range args {
		if arg == "-v" || arg == "--version" || arg == "version" {
			printVersion()
			os.Exit(0)
		}
	}

	if len(args) < 2 {
		log.Fatal("Error: Missing arguments. Usage: monitor <PID> <LIMIT> [TIMEOUT]")
	}

	if len(args) > 3 {
		log.Fatal("Error: Too many arguments. Usage: monitor <PID> <LIMIT> [TIMEOUT]")
	}

	// parse PID
	pidInt, err := strconv.Atoi(args[0])
	if err != nil {
		log.Fatalf("Error: Invalid PID '%s'. Must be an integer.", args[0])
	}

	// parse limit
	limit, err := parseMemoryLimit(args[1])
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	// parse timeout (optional)
	timeout := defaultTimeout
	if len(args) == 3 {
		timeout, err = time.ParseDuration(args[2])
		if err != nil {
			log.Fatalf("Error: Invalid timeout format '%s' (use '5s', '1m').", args[2])
		}
	}

	watchAndKill(int32(pidInt), limit, timeout)
}
