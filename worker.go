package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// RequestResult holds the outcome of a single HTTP request.
type RequestResult struct {
	StatusCode    int
	Duration      time.Duration
	Error         error
	ContentLength int64
}

// Worker performs HTTP requests using a shared client for connection reuse.
type Worker struct {
	client *http.Client
	config *Config
}

// SendRequest executes a single HTTP request and returns the result.
// The requestIndex is used by the template engine to generate per-request
// dynamic values (e.g. {{$sequence}} uses the index directly).
func (w *Worker) SendRequest(ctx context.Context, requestIndex int) RequestResult {
	// Render the URL template. When no placeholders exist this returns
	// the original static URL without allocation.
	targetURL := w.config.URLTemplate.Render(requestIndex)

	// Build the request body from the body template.
	var body io.Reader
	if (w.config.Method == http.MethodPost || w.config.Method == http.MethodPut) && w.config.Body != "" {
		renderedBody := w.config.BodyTemplate.Render(requestIndex)
		body = bytes.NewBufferString(renderedBody)
	}

	req, err := http.NewRequestWithContext(ctx, w.config.Method, targetURL, body)
	if err != nil {
		return RequestResult{
			Error: err,
		}
	}

	for key, value := range w.config.Headers {
		req.Header.Set(key, value)
	}

	start := time.Now()
	resp, err := w.client.Do(req)
	duration := time.Since(start)

	if err != nil {
		return RequestResult{
			Duration: duration,
			Error:    err,
		}
	}
	defer resp.Body.Close()

	contentLength, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return RequestResult{
			Duration: duration,
			Error:    fmt.Errorf("reading response body: %w", err),
		}
	}

	return RequestResult{
		StatusCode:    resp.StatusCode,
		Duration:      duration,
		ContentLength: contentLength,
	}
}

// RunLoadTest orchestrates the load test using a fixed worker pool pattern.
// It dispatches NumRequests jobs across Concurrency goroutines, each reusing
// a shared Transport for connection pooling, and records every result into stats.
// The context can be used to cancel the test early (e.g. on SIGINT).
func RunLoadTest(ctx context.Context, config *Config, stats *Stats) error {
	transport := &http.Transport{
		MaxIdleConns:        config.Concurrency + 10,
		MaxIdleConnsPerHost: config.Concurrency + 10,
		IdleConnTimeout:     30 * time.Second,
		DisableKeepAlives:   false,
	}

	client := &http.Client{
		Timeout:   config.Timeout,
		Transport: transport,
	}

	jobs := make(chan int, config.Concurrency*2)

	var wg sync.WaitGroup

	// Launch a fixed pool of worker goroutines.
	for i := 0; i < config.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := &Worker{client: client, config: config}
			for requestIndex := range jobs {
				result := worker.SendRequest(ctx, requestIndex)
				stats.Record(result)
			}
		}()
	}

	// Dispatch all request indices into the jobs channel.
	for i := 0; i < config.NumRequests; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)

	// Wait for every worker goroutine to finish.
	wg.Wait()

	return nil
}
