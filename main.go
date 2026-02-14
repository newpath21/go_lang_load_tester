package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	config, err := ParseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr, "Usage: go-load-tester -url <URL> [-n requests] [-c concurrency] [-method METHOD] [-timeout duration] [-header 'Key: Value'] [-body 'data']")
		os.Exit(1)
	}

	PrintBanner(config)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stats := NewStats(config.NumRequests)
	done := make(chan struct{})
	progressDone := make(chan struct{})

	go func() {
		StartProgressMonitor(stats, done)
		close(progressDone)
	}()

	if err := RunLoadTest(ctx, config, stats); err != nil {
		fmt.Fprintf(os.Stderr, "\nError running load test: %v\n", err)
	}

	close(done)
	<-progressDone

	summary := stats.GetSummary()
	PrintSummary(summary)
}
