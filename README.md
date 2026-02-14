# Go Load Tester

A lightweight HTTP load testing tool written in Go with zero external dependencies. It sends a configurable number of concurrent requests to a target URL and reports latency percentiles, throughput, status code distribution, and errors.

## Requirements

- Go 1.21+

## Installation

```bash
git clone <repo-url>
cd go_lang_load_tester
go build -o load-tester .
```

## Usage

```
./load-tester -url <URL> [options]
```

### Flags

| Flag       | Default | Description                                      |
|------------|---------|--------------------------------------------------|
| `-url`     | *(required)* | Target URL (must be http or https)          |
| `-n`       | `100`   | Total number of requests to send                 |
| `-c`       | `10`    | Number of concurrent workers (1-100)             |
| `-method`  | `GET`   | HTTP method: GET, POST, PUT, DELETE              |
| `-timeout` | `10s`   | Per-request timeout (e.g. `5s`, `500ms`)         |
| `-header`  | *(none)* | Custom header in `Key: Value` format (repeatable)|
| `-body`    | *(none)* | Request body for POST/PUT requests              |

### Examples

**Simple GET test:**
```bash
./load-tester -url https://example.com -n 500 -c 50
```

**POST with JSON body and headers:**
```bash
./load-tester -url https://api.example.com/login \
  -method POST \
  -n 200 -c 20 \
  -header "Content-Type: application/json" \
  -header "Authorization: Bearer mytoken" \
  -body '{"username":"test","password":"pass123"}' \
  -timeout 5s
```

### Graceful shutdown

Press `Ctrl+C` during a test to stop early. The tool will cancel in-flight requests, wait for workers to finish, and still print a summary of the results collected so far.

## Output

The tool displays a live progress bar during the test, followed by a results summary:

```
══════════════════════════════════════════
 Results
══════════════════════════════════════════
Total Requests:    500
Successful:        500
Failed:            0
Total Time:        2.34s
Requests/sec:      213.68

Latency Distribution:
  Average:   45.12ms
  Min:       12.19ms
  Max:       198.57ms
  P50:       38.20ms
  P90:       89.44ms
  P95:       112.37ms
  P99:       185.21ms

Status Code Distribution:
  [200] 500 responses

Total Data Received: 256.50 KB
```

## Architecture

```
main.go      Orchestration: parse config, wire components, signal handling
config.go    CLI flag parsing and validation
worker.go    Concurrent worker pool with shared HTTP transport
stats.go     Thread-safe metrics collection and percentile computation
ui.go        Progress bar and results formatting
```

All workers share a single `http.Transport` for TCP/TLS connection reuse. Statistics are collected via mutex-protected `Record()` calls and percentiles are computed using the nearest-rank method on a sorted copy of all recorded durations.

## Limitations

- Request body is static (same payload for every request)
- No support for request templating or dynamic parameters
- Results are printed to stdout only (no file/JSON export)
