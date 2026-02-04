# memlimit

Watches the memory usage of a process and its children.
If the total memory usage (evaluated as RSS) exceeds a specified limit, the process tree is terminated (SIGTERM then SIGKILL).

## Installation

### From Source

Requirements: Go 1.20+

#### Direct installation

```bash
go install github.com/audivir/memlimit@latest
```

#### Manual installation

```bash
# Clone the repository
git clone https://github.com/audivir/memlimit
cd memlimit

# Build the binary
go build -ldflags "-s -w" -o memlimit main.go
```

## Usage

```bash
./memlimit <PID> <LIMIT> [TIMEOUT]
```

### Arguments

1. **PID**: The Process ID (integer) to monitor.
2. **LIMIT**: Memory limit with unit (K, M, G, T).
   Optional B suffix. Case insensitive.
3. **TIMEOUT** _(Optional)_: Duration to wait for graceful shutdown (SIGTERM)
   before force killing (SIGKILL). Default: 1s.
   Format examples: 5s, 1m, 500ms. Set to 0s for immediate kill.

### Examples

**1. Force kill immediately if usage exceeds 500MB:**

```bash
./memlimit 12345 500MB 0s

```

**2. Monitor a process with a 2GB limit, giving it 10 seconds to clean up:**

```bash
./memlimit 9999 2GB 10s
```

**3. Standard usage (Default 1s timeout):**

```bash
./memlimit 8888 100M
```

## Testing

The project includes unit tests for parsing logic and an integration test to verify kill signals and timeout behavior.

```bash
go test -v
```

## Dependencies

- [gopsutil](https://github.com/shirou/gopsutil): Used for cross-platform process and system monitoring.

## License

[MIT](LICENSE)
