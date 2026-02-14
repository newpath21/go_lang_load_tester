package main

import (
	"fmt"
	"strings"
	"time"
)

// PrintBanner displays the load tester header with the current configuration.
// When dynamic templates are in use, it lists the detected placeholders.
func PrintBanner(config *Config) {
	fmt.Println("══════════════════════════════════════════")
	fmt.Println(" Go Load Tester")
	fmt.Println("══════════════════════════════════════════")
	fmt.Printf("Target:      %s\n", config.URL)
	fmt.Printf("Requests:    %d\n", config.NumRequests)
	fmt.Printf("Concurrency: %d\n", config.Concurrency)
	fmt.Printf("Method:      %s\n", config.Method)

	// Show dynamic URL template info when placeholders are detected.
	if config.URLTemplate != nil && config.URLTemplate.HasPlaceholders() {
		fmt.Printf("Dynamic URL: enabled (%s)\n", strings.Join(config.URLTemplate.Placeholders(), ", "))
	}

	// Show dynamic body template info when placeholders are detected.
	if config.BodyTemplate != nil && config.BodyTemplate.HasPlaceholders() {
		fmt.Printf("Dynamic Body: enabled (%s)\n", strings.Join(config.BodyTemplate.Placeholders(), ", "))
	}

	fmt.Println("══════════════════════════════════════════")
}

// StartProgressMonitor runs in a goroutine and prints a live progress bar
// every 200ms until the done channel is closed.
func StartProgressMonitor(stats *Stats, done chan struct{}) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			completed, total, elapsed := stats.Progress()
			printProgressBar(completed, total, elapsed)
		case <-done:
			// Print a final 100% progress line before returning.
			completed, total, elapsed := stats.Progress()
			_ = completed
			printProgressBar(total, total, elapsed)
			fmt.Println() // Move to the next line after the progress bar.
			return
		}
	}
}

// printProgressBar renders a single progress line using carriage return.
func printProgressBar(completed, total int, elapsed time.Duration) {
	var pct float64
	if total > 0 {
		pct = float64(completed) / float64(total) * 100
	}

	filled := 0
	if total > 0 {
		filled = int(float64(completed) / float64(total) * 50)
	}
	if filled > 50 {
		filled = 50
	}

	bar := strings.Repeat("#", filled) + strings.Repeat(" ", 50-filled)
	fmt.Printf("\r  Progress: [%-50s] %d/%d (%.1f%%) | Elapsed: %s", bar, completed, total, pct, elapsed.Round(time.Millisecond))
}

// PrintSummary displays the final results table after the load test completes.
func PrintSummary(summary Summary) {
	fmt.Println()
	fmt.Println("══════════════════════════════════════════")
	fmt.Println(" Results")
	fmt.Println("══════════════════════════════════════════")
	fmt.Printf("Total Requests:    %d\n", summary.TotalRequests)
	fmt.Printf("Successful:        %d\n", summary.SuccessCount)
	fmt.Printf("Failed:            %d\n", summary.FailCount)
	fmt.Printf("Total Time:        %s\n", formatDuration(summary.TotalTime))
	fmt.Printf("Requests/sec:      %.2f\n", summary.RequestsPerSec)

	fmt.Println()
	fmt.Println("Latency Distribution:")
	fmt.Printf("  Average:   %s\n", formatDuration(summary.AvgDuration))
	fmt.Printf("  Min:       %s\n", formatDuration(summary.MinDuration))
	fmt.Printf("  Max:       %s\n", formatDuration(summary.MaxDuration))
	fmt.Printf("  P50:       %s\n", formatDuration(summary.P50))
	fmt.Printf("  P90:       %s\n", formatDuration(summary.P90))
	fmt.Printf("  P95:       %s\n", formatDuration(summary.P95))
	fmt.Printf("  P99:       %s\n", formatDuration(summary.P99))

	fmt.Println()
	fmt.Println("Status Code Distribution:")
	for code, count := range summary.StatusCodes {
		fmt.Printf("  [%d] %d responses\n", code, count)
	}

	fmt.Println()
	fmt.Printf("Total Data Received: %s\n", formatBytes(summary.TotalBytes))

	if len(summary.Errors) > 0 {
		fmt.Println()
		fmt.Println("Errors:")
		for _, e := range summary.Errors {
			fmt.Printf("  - %s\n", e)
		}
		if summary.TotalErrors > len(summary.Errors) {
			fmt.Printf("  ... and %d more errors\n", summary.TotalErrors-len(summary.Errors))
		}
	}
}

// formatBytes returns a human-readable byte size string.
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// formatDuration returns a duration formatted as milliseconds if under 1s,
// or as seconds with 2 decimal places otherwise.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}
