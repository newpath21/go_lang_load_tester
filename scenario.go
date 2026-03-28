// scenario.go implements multi-step scenario load testing. It loads a JSON
// scenario file defining a sequence of dependent HTTP requests, runs them
// concurrently with a worker pool, and chains response data between steps
// using variable extraction.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ScenarioStep defines a single HTTP request within a multi-step scenario.
type ScenarioStep struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Extract map[string]string `json:"extract"` // varName -> JSON dot-path

	// Parsed templates (populated by LoadScenario, not from JSON).
	urlTemplate     *Template
	bodyTemplate    *Template
	headerTemplates map[string]*Template
}

// Scenario defines a complete multi-step load test flow.
type Scenario struct {
	Name        string              `json:"name"`
	BaseURL     string              `json:"base_url"`
	Steps       []ScenarioStep      `json:"steps"`
	Concurrency int                 `json:"concurrency"`
	Iterations  int                 `json:"iterations"`
	Users       []map[string]string `json:"users"` // per-iteration credentials/data
}

// LoadScenario reads and validates a scenario JSON file, parsing all templates.
func LoadScenario(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading scenario file: %w", err)
	}

	var s Scenario
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing scenario JSON: %w", err)
	}

	// Validate top-level fields.
	if len(s.Steps) == 0 {
		return nil, fmt.Errorf("scenario must have at least one step")
	}
	if s.Concurrency <= 0 {
		return nil, fmt.Errorf("scenario concurrency must be > 0, got %d", s.Concurrency)
	}
	if s.Iterations <= 0 {
		return nil, fmt.Errorf("scenario iterations must be > 0, got %d", s.Iterations)
	}

	// Validate steps and parse templates.
	validMethods := map[string]bool{
		"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true,
	}
	seenNames := make(map[string]bool)

	for i := range s.Steps {
		step := &s.Steps[i]

		if step.Name == "" {
			return nil, fmt.Errorf("step %d: name is required", i+1)
		}
		if seenNames[step.Name] {
			return nil, fmt.Errorf("step %d: duplicate step name %q", i+1, step.Name)
		}
		seenNames[step.Name] = true

		step.Method = strings.ToUpper(step.Method)
		if step.Method == "" {
			return nil, fmt.Errorf("step %d (%s): method is required", i+1, step.Name)
		}
		if !validMethods[step.Method] {
			return nil, fmt.Errorf("step %d (%s): invalid method %q", i+1, step.Name, step.Method)
		}
		if step.URL == "" {
			return nil, fmt.Errorf("step %d (%s): URL is required", i+1, step.Name)
		}

		// Parse URL template.
		step.urlTemplate, err = ParseTemplate(step.URL)
		if err != nil {
			return nil, fmt.Errorf("step %d (%s) URL: %w", i+1, step.Name, err)
		}

		// Parse body template.
		if step.Body != "" {
			step.bodyTemplate, err = ParseTemplate(step.Body)
			if err != nil {
				return nil, fmt.Errorf("step %d (%s) body: %w", i+1, step.Name, err)
			}
		}

		// Parse header value templates.
		if len(step.Headers) > 0 {
			step.headerTemplates = make(map[string]*Template, len(step.Headers))
			for k, v := range step.Headers {
				tmpl, err := ParseTemplate(v)
				if err != nil {
					return nil, fmt.Errorf("step %d (%s) header %q: %w", i+1, step.Name, k, err)
				}
				step.headerTemplates[k] = tmpl
			}
		}
	}

	return &s, nil
}

// extractJSONPath extracts a value from JSON data using a dot-separated path.
// Uses json.Decoder with UseNumber() to preserve numeric formatting.
func extractJSONPath(data []byte, path string) (string, error) {
	var raw interface{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return "", fmt.Errorf("decoding JSON: %w", err)
	}

	parts := strings.Split(path, ".")
	current := raw

	for i, part := range parts {
		obj, ok := current.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("path %q: at %q (segment %d), expected object but got %T", path, part, i+1, current)
		}
		val, exists := obj[part]
		if !exists {
			return "", fmt.Errorf("path %q: key %q not found at segment %d", path, part, i+1)
		}
		current = val
	}

	switch v := current.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case nil:
		return "", fmt.Errorf("path %q: value is null", path)
	default:
		// For nested objects/arrays, marshal back to JSON string.
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("path %q: marshaling value: %w", path, err)
		}
		return string(b), nil
	}
}

// RunScenario executes a multi-step scenario with a worker pool.
// Each iteration runs all steps sequentially, chaining extracted variables.
// The requestIndex (iteration index) is shared across all steps in one
// iteration so that $sequence produces consistent values.
func RunScenario(ctx context.Context, scenario *Scenario, config *Config, overallStats *Stats, stepStats map[string]*Stats) error {
	transport := &http.Transport{
		MaxIdleConns:        scenario.Concurrency + 10,
		MaxIdleConnsPerHost: scenario.Concurrency + 10,
		IdleConnTimeout:     30 * time.Second,
		DisableKeepAlives:   false,
	}

	client := &http.Client{
		Timeout:   config.Timeout,
		Transport: transport,
	}

	jobs := make(chan int, scenario.Concurrency*2)

	var wg sync.WaitGroup

	for i := 0; i < scenario.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iterIndex := range jobs {
				runIteration(ctx, client, scenario, iterIndex, overallStats, stepStats)
			}
		}()
	}

	// Dispatch iteration indices.
	for i := 0; i < scenario.Iterations; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()

	return nil
}

// maxResponseBody is the maximum response body size to read when extracting variables.
const maxResponseBody = 1 << 20 // 1 MB

// runIteration executes all steps of a scenario for a single iteration.
// If any step fails (transport error or non-2xx), remaining steps are skipped.
func runIteration(ctx context.Context, client *http.Client, scenario *Scenario, iterIndex int, overallStats *Stats, stepStats map[string]*Stats) {
	vars := map[string]string{
		"base_url": scenario.BaseURL,
	}
	if len(scenario.Users) > 0 {
		for k, v := range scenario.Users[iterIndex%len(scenario.Users)] {
			vars[k] = v
		}
	}

	var failed bool

	for i := range scenario.Steps {
		step := &scenario.Steps[i]

		// Check for cancellation before each step.
		if ctx.Err() != nil {
			return
		}

		// If a previous step failed, record skip for remaining steps.
		if failed {
			result := RequestResult{
				Error: fmt.Errorf("skipped: previous step failed"),
			}
			overallStats.Record(result)
			if ss, ok := stepStats[step.Name]; ok {
				ss.Record(result)
			}
			continue
		}

		result := executeStep(ctx, client, step, iterIndex, vars)

		overallStats.Record(result)
		if ss, ok := stepStats[step.Name]; ok {
			ss.Record(result)
		}

		if result.Error != nil || (result.StatusCode < 200 || result.StatusCode >= 300) {
			failed = true
			if result.Error == nil {
				// Non-2xx is a logical failure — record it as an error too.
				errResult := RequestResult{
					StatusCode:    result.StatusCode,
					Duration:      result.Duration,
					ContentLength: result.ContentLength,
					Error:         fmt.Errorf("step %q: HTTP %d", step.Name, result.StatusCode),
				}
				// Re-record with error for the overall stats error list.
				// The status code was already recorded above, so just add the error info.
				_ = errResult // error is already visible from the non-2xx status code
			}
		}
	}
}

// executeStep runs a single scenario step, rendering templates, making the
// HTTP request, and extracting variables from the response.
func executeStep(ctx context.Context, client *http.Client, step *ScenarioStep, iterIndex int, vars map[string]string) RequestResult {
	// Render URL.
	targetURL := step.urlTemplate.RenderWithVars(iterIndex, vars)

	// Render body.
	var body io.Reader
	if step.bodyTemplate != nil {
		renderedBody := step.bodyTemplate.RenderWithVars(iterIndex, vars)
		body = bytes.NewBufferString(renderedBody)
	}

	req, err := http.NewRequestWithContext(ctx, step.Method, targetURL, body)
	if err != nil {
		return RequestResult{Error: fmt.Errorf("step %q: creating request: %w", step.Name, err)}
	}

	// Render and set headers.
	for key, tmpl := range step.headerTemplates {
		req.Header.Set(key, tmpl.RenderWithVars(iterIndex, vars))
	}

	start := time.Now()
	resp, err := client.Do(req)
	duration := time.Since(start)

	if err != nil {
		return RequestResult{
			Duration: duration,
			Error:    fmt.Errorf("step %q: %w", step.Name, err),
		}
	}
	defer resp.Body.Close()

	// If we need to extract variables, read the body; otherwise discard.
	var contentLength int64
	if len(step.Extract) > 0 {
		bodyData, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		if err != nil {
			return RequestResult{
				StatusCode: resp.StatusCode,
				Duration:   duration,
				Error:      fmt.Errorf("step %q: reading response: %w", step.Name, err),
			}
		}
		contentLength = int64(len(bodyData))

		// Only extract if status is 2xx.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			for varName, jsonPath := range step.Extract {
				val, err := extractJSONPath(bodyData, jsonPath)
				if err != nil {
					return RequestResult{
						StatusCode:    resp.StatusCode,
						Duration:      duration,
						ContentLength: contentLength,
						Error:         fmt.Errorf("step %q: extracting %q: %w", step.Name, varName, err),
					}
				}
				vars[varName] = val
			}
		}
	} else {
		contentLength, err = io.Copy(io.Discard, resp.Body)
		if err != nil {
			return RequestResult{
				StatusCode: resp.StatusCode,
				Duration:   duration,
				Error:      fmt.Errorf("step %q: reading response: %w", step.Name, err),
			}
		}
	}

	return RequestResult{
		StatusCode:    resp.StatusCode,
		Duration:      duration,
		ContentLength: contentLength,
	}
}
