// stats.go implements the statistics collection and aggregation engine.
// It tracks per-request metrics in a thread-safe manner and produces
// a final Summary with percentile latencies, throughput, and error info.
package main

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Stats collects and aggregates metrics from every request in a load test.
// All fields are protected by a mutex so that concurrent workers can safely
// record results without data races.
type Stats struct {
	mu            sync.Mutex
	totalRequests int
	totalErrors   int
	successCount  int
	failCount     int
	statusCodes   map[int]int
	durations     []time.Duration
	totalDuration time.Duration
	minDuration   time.Duration
	maxDuration   time.Duration
	totalBytes    int64
	errors        []string
	startTime     time.Time
	numRequests   int
}

// NewStats creates and initializes a Stats instance for a test expecting
// numRequests total requests. The start time is recorded immediately so
// that wall-clock elapsed time is accurate from the moment the Stats is created.
func NewStats(numRequests int) *Stats {
	return &Stats{
		statusCodes: make(map[int]int),
		durations:   make([]time.Duration, 0, numRequests),
		minDuration: time.Duration(math.MaxInt64),
		startTime:   time.Now(),
		numRequests: numRequests,
	}
}

// Record ingests a single RequestResult into the running statistics.
// It is safe to call from multiple goroutines concurrently.
func (s *Stats) Record(result RequestResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests++

	if result.Error != nil {
		s.failCount++
		s.totalErrors++
		if len(s.errors) < 10 {
			s.errors = append(s.errors, result.Error.Error())
		}
	} else {
		s.successCount++
		s.statusCodes[result.StatusCode]++
	}

	s.totalDuration += result.Duration

	if result.Duration < s.minDuration {
		s.minDuration = result.Duration
	}
	if result.Duration > s.maxDuration {
		s.maxDuration = result.Duration
	}

	s.durations = append(s.durations, result.Duration)
	s.totalBytes += result.ContentLength
}

// Progress returns the current completion count, total expected requests,
// and time elapsed since the test started. It is safe for concurrent use.
func (s *Stats) Progress() (completed int, total int, elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.totalRequests, s.numRequests, time.Since(s.startTime)
}

// Summary holds the final, fully-computed results of a load test.
// It is an exported value type intended for the UI layer to consume.
type Summary struct {
	TotalRequests  int
	SuccessCount   int
	FailCount      int
	TotalErrors    int
	TotalTime      time.Duration
	AvgDuration    time.Duration
	MinDuration    time.Duration
	MaxDuration    time.Duration
	P50            time.Duration
	P90            time.Duration
	P95            time.Duration
	P99            time.Duration
	RequestsPerSec float64
	StatusCodes    map[int]int
	TotalBytes     int64
	Errors         []string
}

// GetSummary computes and returns a Summary snapshot of the current statistics.
// It sorts a copy of the recorded durations to calculate percentile latencies
// and derives throughput from the wall-clock elapsed time.
func (s *Stats) GetSummary() Summary {
	s.mu.Lock()
	defer s.mu.Unlock()

	elapsed := time.Since(s.startTime)

	// Sort a copy of durations so we don't mutate internal state.
	sorted := make([]time.Duration, len(s.durations))
	copy(sorted, s.durations)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	// Compute minDuration locally without mutating the field.
	minDur := s.minDuration
	if minDur == time.Duration(math.MaxInt64) {
		minDur = 0
	}

	var avgDuration time.Duration
	if s.totalRequests > 0 {
		avgDuration = s.totalDuration / time.Duration(s.totalRequests)
	}

	var reqPerSec float64
	if elapsed.Seconds() > 0 {
		reqPerSec = float64(s.totalRequests) / elapsed.Seconds()
	}

	// Copy the status codes map so the caller cannot mutate internal state.
	codes := make(map[int]int, len(s.statusCodes))
	for k, v := range s.statusCodes {
		codes[k] = v
	}

	// Copy the errors slice for the same reason.
	errs := make([]string, len(s.errors))
	copy(errs, s.errors)

	summary := Summary{
		TotalRequests:  s.totalRequests,
		SuccessCount:   s.successCount,
		FailCount:      s.failCount,
		TotalErrors:    s.totalErrors,
		TotalTime:      elapsed,
		AvgDuration:    avgDuration,
		MinDuration:    minDur,
		MaxDuration:    s.maxDuration,
		P50:            percentile(sorted, 50),
		P90:            percentile(sorted, 90),
		P95:            percentile(sorted, 95),
		P99:            percentile(sorted, 99),
		RequestsPerSec: reqPerSec,
		StatusCodes:    codes,
		TotalBytes:     s.totalBytes,
		Errors:         errs,
	}

	return summary
}

// percentile returns the value at the given percentile from a sorted slice
// of durations using the nearest-rank method. If the slice is empty it returns zero.
func percentile(sorted []time.Duration, pct float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(pct/100*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
