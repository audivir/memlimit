package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// unit tests

func TestParseMemoryLimit(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
		hasError bool
	}{
		{"100B", 0, true}, // Invalid unit
		{"1KB", 1024, false},
		{"10mb", 10 * 1024 * 1024, false},
		{"1G", 1024 * 1024 * 1024, false},
		{"invalid", 0, true},
	}

	for _, test := range tests {
		val, err := parseMemoryLimit(test.input)
		if test.hasError {
			if err == nil {
				t.Errorf("Expected error for '%s', got nil", test.input)
			}
		} else {
			if val != test.expected {
				t.Errorf("For '%s', expected %d, got %d", test.input, test.expected, val)
			}
		}
	}
}

func TestFormatBytes(t *testing.T) {
	if res := formatBytes(1024); res != "1.00 KB" {
		t.Errorf("Expected 1.00 KB, got %s", res)
	}
	if res := formatBytes(1024 * 1024 * 500); res != "500.00 MB" {
		t.Errorf("Expected 500.00 MB, got %s", res)
	}
}

// integration tests

// TestHelperProcess is a helper "victim" program invoked
// by the other tests to simulate a memory leak.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_PROCESS") != "1" {
		return
	}

	// optional: ignore SIGTERM to test force kill capability
	if os.Getenv("IGNORE_SIGTERM") == "1" {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		go func() {
			for range c {
				fmt.Println("Victim: Ignoring SIGTERM...")
			}
		}()
	}

	fmt.Printf("Victim: Started with PID %d\n", os.Getpid())

	// allocate memory (50MB)
	// use a large slice to spike RSS
	blockSize := 50 * 1024 * 1024 // 50MB
	_ = make([]byte, blockSize)

	// touch memory to ensure OS actually allocates RSS
	// (Go sometimes lazy allocates)
	data := make([]byte, blockSize)
	for i := 0; i < len(data); i += 4096 {
		data[i] = 1
	}
	fmt.Println("Victim: Memory allocated.")

	// keep alive until killed
	select {}
}
func TestWatchAndKill(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("Skipping integration test: cannot find executable")
	}

	tests := []struct {
		name          string
		ignoreSigterm bool
		expectForce   bool // Should we see force kill messages?
	}{
		{
			name:          "StandardKill",
			ignoreSigterm: false,
			expectForce:   false,
		},
		{
			name:          "ForceKillAfterTimeout",
			ignoreSigterm: true,
			expectForce:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// capture stdout (pipe)
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// restore stdout after test
			defer func() {
				os.Stdout = oldStdout
			}()

			// start victim
			cmd := exec.Command(exe, "-test.run=TestHelperProcess")
			cmd.Env = append(os.Environ(), "GO_TEST_PROCESS=1")
			if tc.ignoreSigterm {
				cmd.Env = append(cmd.Env, "IGNORE_SIGTERM=1")
			}

			// Pipe victim output into our captured stdout as well
			cmd.Stdout = w
			cmd.Stderr = w

			if err := cmd.Start(); err != nil {
				t.Fatalf("Failed to start victim: %v", err)
			}

			pid := int32(cmd.Process.Pid)

			// debug output
			fmt.Printf("\n=== Test Start: PID %d ===\n", pid)

			// run watcher
			done := make(chan bool)
			go func() {
				// give victim time to allocate memory
				time.Sleep(500 * time.Millisecond)

				// limit 10MB, timeout 1s
				watchAndKill(pid, 10*1024*1024, 1*time.Second)
				done <- true
			}()

			// wait for watcher to complete
			select {
			case <-done:
				// Success
			case <-time.After(5 * time.Second):
				// close pipe and read output
				w.Close()
				out, _ := io.ReadAll(r)
				fmt.Fprintf(oldStdout, "Captured Output:\n%s\n", string(out))
				t.Fatal("Timeout: watchAndKill did not return")
			}

			// reap zombie
			_ = cmd.Wait()

			// close pipe and read output
			w.Close()
			outBytes, _ := io.ReadAll(r)
			output := string(outBytes)

			// print captured output to real stdout for debugging
			if testing.Verbose() {
				fmt.Fprintf(oldStdout, "%s", output)
			}

			// assertions
			// verify process is gone
			if runtime.GOOS != "windows" {
				p, _ := os.FindProcess(int(pid))
				if err := p.Signal(syscall.Signal(0)); err == nil {
					t.Error("Victim process is still alive!")
					_ = p.Kill()
				}
			} // TODO: windows implementation

			// verify log messages
			forceMsg1 := "Timeout reached. Force killing..."
			forceMsg2 := "Process tree force killed."

			if tc.expectForce {
				if !strings.Contains(output, forceMsg1) {
					t.Errorf("Expected log message %q not found in output.", forceMsg1)
				}
				if !strings.Contains(output, forceMsg2) {
					t.Errorf("Expected log message %q not found in output.", forceMsg2)
				}
			} else {
				if strings.Contains(output, forceMsg1) {
					t.Errorf("Unexpected log message %q found in test.", forceMsg1)
				}
			}
		})
	}
}
