# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
# Build binary
go build -o load-tester .

# Run (minimum flags)
./load-tester -url https://example.com

# Full options
./load-tester -url https://example.com -n 1000 -c 100 -method POST \
  -header "Authorization: Bearer token" -header "Content-Type: application/json" \
  -body '{"key":"value"}' -timeout 5s
```

No external dependencies — uses only Go standard library. No test suite exists yet.

## Architecture

Single-package (`main`) CLI tool with four layers that execute sequentially:

```
config.go → worker.go → stats.go → ui.go
```

**Config** (`config.go`): Parses CLI flags via `flag` package into a `Config` struct. Custom `headerFlags` type implements `flag.Value` to support repeated `-header` flags. Validates URL, concurrency (1-100), method, and timeout.

**Worker Pool** (`worker.go`): `RunLoadTest()` spawns a fixed pool of `Config.Concurrency` goroutines. Each goroutine owns one `Worker` (with its own `http.Client`) for TCP/TLS connection reuse. Jobs are dispatched through a buffered channel (`concurrency*2` capacity). Each `SendRequest()` drains the response body via `io.Copy(io.Discard, ...)` to ensure connections return to the pool.

**Stats** (`stats.go`): `Stats` struct uses `sync.Mutex` to safely accept `Record()` calls from all concurrent workers. Stores every request duration in a slice. `GetSummary()` sorts the slice to compute P50/P90/P95/P99 percentiles by index lookup, then returns a snapshot `Summary` struct with copied maps/slices.

**UI** (`ui.go`): `StartProgressMonitor()` runs in a separate goroutine with a 200ms `time.Ticker`, reading `Stats.Progress()` and rendering a `\r`-overwritten progress bar. `PrintSummary()` formats the final `Summary` into a results table.

**Orchestration** (`main.go`): `main()` wires the layers: parse config → print banner → create stats → start progress goroutine → run load test → close done channel → print summary.

## Key Type Flow

`Config` → passed to `Worker` and `UI` functions
`Worker.SendRequest()` → returns `RequestResult` (status, duration, error, content length)
`Stats.Record(RequestResult)` → accumulates metrics thread-safely
`Stats.GetSummary()` → returns `Summary` (percentiles, throughput, status codes)
`PrintSummary(Summary)` → formatted console output
