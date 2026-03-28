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
		fmt.Fprintln(os.Stderr, "       go-load-tester -scenario <file.json> [-timeout duration]")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Scenario mode: multi-step flow.
	if config.ScenarioFile != "" {
		scenario, err := LoadScenario(config.ScenarioFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		PrintScenarioBanner(scenario)

		// Total requests = iterations * steps.
		totalRequests := scenario.Iterations * len(scenario.Steps)
		overallStats := NewStats(totalRequests)

		// Per-step stats.
		perStepStats := make(map[string]*Stats, len(scenario.Steps))
		for _, step := range scenario.Steps {
			perStepStats[step.Name] = NewStats(scenario.Iterations)
		}

		done := make(chan struct{})
		progressDone := make(chan struct{})
		go func() {
			StartProgressMonitor(overallStats, done)
			close(progressDone)
		}()

		if err := RunScenario(ctx, scenario, config, overallStats, perStepStats); err != nil {
			fmt.Fprintf(os.Stderr, "\nError running scenario: %v\n", err)
		}

		close(done)
		<-progressDone

		overall := overallStats.GetSummary()
		PrintScenarioSummary(overall, scenario, perStepStats)
		return
	}

	// Single-request mode.
	PrintBanner(config)

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
