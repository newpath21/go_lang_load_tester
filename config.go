// config.go defines the configuration layer for the load tester.
// It parses CLI flags, validates inputs, and returns a Config struct
// that the rest of the application uses.
package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config holds all configuration for a load test run.
type Config struct {
	URL         string            // Target URL to test
	NumRequests int               // Total number of requests to send
	Concurrency int               // Number of concurrent workers
	Method      string            // HTTP method: GET, POST, PUT, DELETE
	Timeout     time.Duration     // Per-request timeout
	Headers     map[string]string // Custom HTTP headers
	Body        string            // Request body for POST/PUT

	// BodyTemplate is the parsed template for the request body. When it
	// contains dynamic placeholders, each request gets a unique body.
	BodyTemplate *Template
	// URLTemplate is the parsed template for the target URL. When it
	// contains dynamic placeholders, each request targets a unique URL.
	URLTemplate *Template
}

// headerFlags is a custom flag type that allows multiple -header flags.
// It implements the flag.Value interface so the flag package can accumulate
// repeated -header values into a single slice.
type headerFlags []string

// String returns a string representation of the collected headers.
func (h *headerFlags) String() string {
	return strings.Join(*h, ", ")
}

// Set appends a new header value each time -header is provided on the CLI.
func (h *headerFlags) Set(value string) error {
	*h = append(*h, value)
	return nil
}

// ParseConfig parses command-line flags and returns a validated Config.
// It returns an error with a clear message if any validation fails.
func ParseConfig() (*Config, error) {
	fs := flag.NewFlagSet("load-tester", flag.ContinueOnError)

	urlFlag := fs.String("url", "", "Target URL to load test (required)")
	numRequests := fs.Int("n", 100, "Total number of requests to send")
	concurrency := fs.Int("c", 10, "Number of concurrent workers (1-100)")
	method := fs.String("method", "GET", "HTTP method: GET, POST, PUT, DELETE")
	timeout := fs.String("timeout", "10s", "Per-request timeout (e.g. 5s, 500ms)")
	body := fs.String("body", "", "Request body for POST/PUT requests")

	var headers headerFlags
	fs.Var(&headers, "header", "Custom header in 'Key: Value' format (can be repeated)")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	// --- Validation ---

	// URL is required.
	if *urlFlag == "" {
		return nil, fmt.Errorf("validation error: -url flag is required")
	}

	// Validate URL has a proper http/https scheme.
	// When the URL contains {{...}} template placeholders, replace them with
	// dummy values before parsing so that url.ParseRequestURI succeeds.
	urlToValidate := stripTemplatePlaceholders(*urlFlag)
	parsed, err := url.ParseRequestURI(urlToValidate)
	if err != nil {
		return nil, fmt.Errorf("validation error: invalid URL %q: %w", *urlFlag, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("validation error: URL scheme must be http or https, got %q", parsed.Scheme)
	}

	// Number of requests must be at least 1.
	if *numRequests < 1 {
		return nil, fmt.Errorf("validation error: -n (number of requests) must be >= 1, got %d", *numRequests)
	}

	// Concurrency must be between 1 and 100.
	if *concurrency < 1 || *concurrency > 100 {
		return nil, fmt.Errorf("validation error: -c (concurrency) must be between 1 and 100, got %d", *concurrency)
	}

	// Method must be one of the allowed HTTP methods.
	allowedMethods := map[string]bool{
		"GET":    true,
		"POST":   true,
		"PUT":    true,
		"DELETE": true,
	}
	upperMethod := strings.ToUpper(*method)
	if !allowedMethods[upperMethod] {
		return nil, fmt.Errorf("validation error: -method must be one of GET, POST, PUT, DELETE, got %q", *method)
	}

	// Parse the timeout duration string.
	dur, err := time.ParseDuration(*timeout)
	if err != nil {
		return nil, fmt.Errorf("validation error: invalid -timeout value %q: %w", *timeout, err)
	}

	// Parse custom headers from "Key: Value" format into a map.
	headerMap := make(map[string]string)
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("validation error: invalid header format %q, expected 'Key: Value'", h)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, fmt.Errorf("validation error: header key must not be empty in %q", h)
		}
		headerMap[key] = value
	}

	// Parse the body template to detect and validate dynamic placeholders.
	bodyTmpl, err := ParseTemplate(*body)
	if err != nil {
		return nil, fmt.Errorf("validation error: invalid body template: %w", err)
	}

	// Parse the URL template to detect and validate dynamic placeholders.
	urlTmpl, err := ParseTemplate(*urlFlag)
	if err != nil {
		return nil, fmt.Errorf("validation error: invalid URL template: %w", err)
	}

	return &Config{
		URL:          *urlFlag,
		NumRequests:  *numRequests,
		Concurrency:  *concurrency,
		Method:       upperMethod,
		Timeout:      dur,
		Headers:      headerMap,
		Body:         *body,
		BodyTemplate: bodyTmpl,
		URLTemplate:  urlTmpl,
	}, nil
}

// stripTemplatePlaceholders replaces all {{...}} tokens with a dummy value
// so that URL validation can succeed even when the URL contains dynamic
// template placeholders like {{$randomInt}}.
func stripTemplatePlaceholders(s string) string {
	result := s
	for {
		openIdx := strings.Index(result, "{{")
		if openIdx == -1 {
			break
		}
		closeIdx := strings.Index(result[openIdx:], "}}")
		if closeIdx == -1 {
			break
		}
		closeIdx += openIdx
		result = result[:openIdx] + "0" + result[closeIdx+2:]
	}
	return result
}
