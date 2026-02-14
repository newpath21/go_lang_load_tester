// dynamic.go implements a template engine for generating per-request dynamic
// values. It parses a template string containing {{$placeholder}} tokens at
// startup, then efficiently generates a fresh body/URL for each request by
// replacing placeholders with values from built-in generators.
package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	mathrand "math/rand"
	"strconv"
	"strings"
	"time"
)

// generatorFunc produces a dynamic string value for a single request.
// The requestIndex parameter is the zero-based sequence number of the
// request within the load test run.
type generatorFunc func(requestIndex int) string

// templateSegment represents either a static text fragment or a dynamic
// placeholder within a parsed template. Exactly one of staticText or
// generator is used per segment.
type templateSegment struct {
	staticText string
	generator  generatorFunc
	name       string // placeholder name (e.g. "$uuid"), empty for static segments
}

// Template is a parsed template that can efficiently render per-request
// strings by concatenating static segments and dynamic generator outputs.
type Template struct {
	segments     []templateSegment
	placeholders []string // unique placeholder names found in the template
	raw          string   // original unparsed template string
}

// splitPlaceholder splits a raw placeholder text into a base name and a
// parameter string. For example:
//
//	"$sequence(1,3)" → "$sequence", "1,3"
//	"$uuid"          → "$uuid",    ""
//	"$sequence()"    → "$sequence", ""
func splitPlaceholder(raw string) (baseName, params string, err error) {
	openParen := strings.Index(raw, "(")
	if openParen == -1 {
		// No parentheses — simple placeholder.
		return raw, "", nil
	}
	if !strings.HasSuffix(raw, ")") {
		return "", "", fmt.Errorf("unbalanced parentheses in placeholder %q", raw)
	}
	baseName = raw[:openParen]
	params = raw[openParen+1 : len(raw)-1]
	return baseName, params, nil
}

// parseIntParams parses a comma-separated list of integer parameters.
// It accepts up to len(defaults) values; missing or empty values use the
// corresponding default. It returns an error if more than len(defaults) values
// are supplied or if a value is not a valid integer.
func parseIntParams(raw string, defaults ...int) ([]int, error) {
	result := make([]int, len(defaults))
	copy(result, defaults)

	if raw == "" {
		return result, nil
	}

	parts := strings.Split(raw, ",")
	if len(parts) > len(defaults) {
		return nil, fmt.Errorf("expected at most %d parameters, got %d", len(defaults), len(parts))
	}

	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue // use default
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid integer parameter %q", p)
		}
		result[i] = v
	}

	return result, nil
}

// noParams is a helper that returns an error if a parameterless generator
// receives parameters.
func noParams(name, params string) error {
	if params != "" {
		return fmt.Errorf("%s does not accept parameters", name)
	}
	return nil
}

// ParseTemplate parses a template string and returns a Template.
// Placeholders use the syntax {{$name}}. Unknown placeholders cause an error.
// If the template contains no placeholders, Render returns the original
// string without allocations (the fast path).
func ParseTemplate(raw string) (*Template, error) {
	t := &Template{raw: raw}
	seen := make(map[string]bool)

	remaining := raw
	for {
		openIdx := strings.Index(remaining, "{{")
		if openIdx == -1 {
			// No more placeholders; append the rest as static text.
			if len(remaining) > 0 {
				t.segments = append(t.segments, templateSegment{staticText: remaining})
			}
			break
		}

		closeIdx := strings.Index(remaining[openIdx:], "}}")
		if closeIdx == -1 {
			// Unclosed {{ — treat the rest as literal text.
			t.segments = append(t.segments, templateSegment{staticText: remaining})
			break
		}
		closeIdx += openIdx // adjust to absolute position

		// Static text before the placeholder.
		if openIdx > 0 {
			t.segments = append(t.segments, templateSegment{staticText: remaining[:openIdx]})
		}

		// Extract the placeholder (e.g. "$sequence(1,3)" from "{{$sequence(1,3)}}").
		rawPlaceholder := strings.TrimSpace(remaining[openIdx+2 : closeIdx])
		baseName, params, err := splitPlaceholder(rawPlaceholder)
		if err != nil {
			return nil, fmt.Errorf("parsing template: %w", err)
		}
		name := baseName
		gen, err := lookupGenerator(baseName, params)
		if err != nil {
			return nil, fmt.Errorf("parsing template: %w", err)
		}

		t.segments = append(t.segments, templateSegment{generator: gen, name: name})
		if !seen[name] {
			seen[name] = true
			t.placeholders = append(t.placeholders, name)
		}

		remaining = remaining[closeIdx+2:]
	}

	return t, nil
}

// HasPlaceholders reports whether the template contains any dynamic placeholders.
func (t *Template) HasPlaceholders() bool {
	return len(t.placeholders) > 0
}

// Placeholders returns the unique placeholder names found in the template.
func (t *Template) Placeholders() []string {
	return t.placeholders
}

// Render generates a concrete string for the given request index by
// evaluating every placeholder generator. If no placeholders exist,
// it returns the original raw string without any allocation.
func (t *Template) Render(requestIndex int) string {
	if !t.HasPlaceholders() {
		return t.raw
	}

	var b strings.Builder
	// Pre-size the builder to roughly the raw length to avoid resizing.
	b.Grow(len(t.raw))

	for i := range t.segments {
		seg := &t.segments[i]
		if seg.generator != nil {
			b.WriteString(seg.generator(requestIndex))
		} else {
			b.WriteString(seg.staticText)
		}
	}

	return b.String()
}

// lookupGenerator returns the generator function for a named placeholder.
// Parameterized placeholders (e.g. $sequence(1,3)) parse their params here
// and return a closure capturing the parsed values. Parameterless generators
// reject non-empty params with a clear error.
func lookupGenerator(name, params string) (generatorFunc, error) {
	switch name {
	case "$uuid":
		if err := noParams(name, params); err != nil {
			return nil, err
		}
		return genUUID, nil

	case "$randomInt":
		p, err := parseIntParams(params, 0, 10000)
		if err != nil {
			return nil, fmt.Errorf("$randomInt: %w", err)
		}
		min, max := p[0], p[1]
		if min > max {
			return nil, fmt.Errorf("$randomInt: min (%d) must be <= max (%d)", min, max)
		}
		if min == 0 && max == 10000 && params == "" {
			return genRandomInt, nil // fast path: default behavior
		}
		return func(_ int) string {
			return fmt.Sprintf("%d", min+mathrand.Intn(max-min+1))
		}, nil

	case "$randomFloat":
		if err := noParams(name, params); err != nil {
			return nil, err
		}
		return genRandomFloat, nil

	case "$timestamp":
		if err := noParams(name, params); err != nil {
			return nil, err
		}
		return genTimestamp, nil

	case "$timestampISO":
		if err := noParams(name, params); err != nil {
			return nil, err
		}
		return genTimestampISO, nil

	case "$randomString":
		p, err := parseIntParams(params, 16)
		if err != nil {
			return nil, fmt.Errorf("$randomString: %w", err)
		}
		length := p[0]
		if length <= 0 {
			return nil, fmt.Errorf("$randomString: length must be > 0, got %d", length)
		}
		if length == 16 && params == "" {
			return genRandomString, nil // fast path: default behavior
		}
		return func(_ int) string {
			b := make([]byte, length)
			for i := range b {
				b[i] = alphanumeric[mathrand.Intn(len(alphanumeric))]
			}
			return string(b)
		}, nil

	case "$randomEmail":
		if err := noParams(name, params); err != nil {
			return nil, err
		}
		return genRandomEmail, nil

	case "$randomName":
		if err := noParams(name, params); err != nil {
			return nil, err
		}
		return genRandomName, nil

	case "$sequence":
		p, err := parseIntParams(params, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("$sequence: %w", err)
		}
		start, pad := p[0], p[1]
		if pad < 0 {
			return nil, fmt.Errorf("$sequence: pad width must be >= 0, got %d", pad)
		}
		if start == 0 && pad == 0 && params == "" {
			return genSequence, nil // fast path: default behavior
		}
		if pad == 0 {
			return func(requestIndex int) string {
				return fmt.Sprintf("%d", start+requestIndex)
			}, nil
		}
		fmtStr := fmt.Sprintf("%%0%dd", pad)
		return func(requestIndex int) string {
			return fmt.Sprintf(fmtStr, start+requestIndex)
		}, nil

	case "$cycle":
		// $cycle(start, count, pad) produces values that wrap around:
		// value = start + (requestIndex % count), zero-padded to pad width.
		// Example: $cycle(1,50,3) → 001,002,...,050,001,002,...
		p, err := parseIntParams(params, 1, 10, 0)
		if err != nil {
			return nil, fmt.Errorf("$cycle: %w", err)
		}
		start, count, pad := p[0], p[1], p[2]
		if count <= 0 {
			return nil, fmt.Errorf("$cycle: count must be > 0, got %d", count)
		}
		if pad < 0 {
			return nil, fmt.Errorf("$cycle: pad width must be >= 0, got %d", pad)
		}
		if pad == 0 {
			return func(requestIndex int) string {
				return fmt.Sprintf("%d", start+(requestIndex%count))
			}, nil
		}
		fmtStr := fmt.Sprintf("%%0%dd", pad)
		return func(requestIndex int) string {
			return fmt.Sprintf(fmtStr, start+(requestIndex%count))
		}, nil

	case "$randomBool":
		if err := noParams(name, params); err != nil {
			return nil, err
		}
		return genRandomBool, nil

	case "$randomIP":
		if err := noParams(name, params); err != nil {
			return nil, err
		}
		return genRandomIP, nil

	case "$randomUA":
		if err := noParams(name, params); err != nil {
			return nil, err
		}
		return genRandomUA, nil

	default:
		return nil, fmt.Errorf("unknown placeholder %q (available: $uuid, $randomInt(min,max), $randomFloat, $timestamp, $timestampISO, $randomString(length), $randomEmail, $randomName, $sequence(start,pad), $cycle(start,count,pad), $randomBool, $randomIP, $randomUA)", name)
	}
}

// --- Built-in Generators ---

// genUUID generates a random UUID v4 string using crypto/rand.
func genUUID(_ int) string {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		// Fallback to math/rand if crypto/rand fails (extremely unlikely).
		for i := range uuid {
			uuid[i] = byte(mathrand.Intn(256))
		}
	}
	// Set version 4 (bits 12-15 of time_hi_and_version).
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	// Set variant bits (bits 6-7 of clk_seq_hi_res).
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// genRandomInt generates a random integer between 0 and 10000.
func genRandomInt(_ int) string {
	return fmt.Sprintf("%d", mathrand.Intn(10001))
}

// genRandomFloat generates a random float between 0.0 and 1.0 with 6 decimal places.
func genRandomFloat(_ int) string {
	return fmt.Sprintf("%.6f", mathrand.Float64())
}

// genTimestamp returns the current Unix timestamp in seconds.
func genTimestamp(_ int) string {
	return fmt.Sprintf("%d", time.Now().Unix())
}

// genTimestampISO returns the current time in ISO 8601 / RFC 3339 format.
func genTimestampISO(_ int) string {
	return time.Now().UTC().Format(time.RFC3339)
}

// alphanumeric is the character set for random string generation.
const alphanumeric = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// genRandomString generates a random 16-character alphanumeric string.
func genRandomString(_ int) string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = alphanumeric[mathrand.Intn(len(alphanumeric))]
	}
	return string(b)
}

// genRandomEmail generates a random email address like user_abc123@example.com.
func genRandomEmail(_ int) string {
	prefix := make([]byte, 8)
	for i := range prefix {
		prefix[i] = alphanumeric[mathrand.Intn(len(alphanumeric))]
	}
	domains := []string{"example.com", "test.com", "demo.org", "mail.example.com"}
	domain := domains[mathrand.Intn(len(domains))]
	return fmt.Sprintf("user_%s@%s", string(prefix), domain)
}

// firstNames is a small built-in list of first names for the $randomName generator.
var firstNames = []string{
	"Alice", "Bob", "Charlie", "Diana", "Eve", "Frank", "Grace", "Henry",
	"Iris", "Jack", "Kate", "Leo", "Mia", "Noah", "Olivia", "Paul",
	"Quinn", "Ruby", "Sam", "Tina", "Uma", "Victor", "Wendy", "Xander",
	"Yara", "Zach",
}

// genRandomName returns a random first name from the built-in list.
func genRandomName(_ int) string {
	return firstNames[mathrand.Intn(len(firstNames))]
}

// genSequence returns the request index as a monotonically increasing integer.
func genSequence(requestIndex int) string {
	return fmt.Sprintf("%d", requestIndex)
}

// genRandomBool returns a random "true" or "false" string.
func genRandomBool(_ int) string {
	if mathrand.Intn(2) == 0 {
		return "false"
	}
	return "true"
}

// genRandomIP generates a random IPv4 address, avoiding reserved ranges.
func genRandomIP(_ int) string {
	// Generate octets in 1-254 range for the first octet to avoid 0.x.x.x and 255.x.x.x.
	o1 := mathrand.Intn(254) + 1
	o2 := mathrand.Intn(256)
	o3 := mathrand.Intn(256)
	o4 := mathrand.Intn(254) + 1
	return fmt.Sprintf("%d.%d.%d.%d", o1, o2, o3, o4)
}

// userAgents is a small built-in list of user-agent strings for the $randomUA generator.
var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
}

// genRandomUA returns a random User-Agent string from the built-in list.
func genRandomUA(_ int) string {
	return userAgents[mathrand.Intn(len(userAgents))]
}

// --- Seed initialization ---

func init() {
	// Seed math/rand with a cryptographically random value so that
	// generator output varies across runs. On Go 1.20+ this is automatic,
	// but we do it explicitly for Go 1.21 compatibility and clarity.
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		// If crypto/rand fails, fall back to time-based seed.
		mathrand.Seed(time.Now().UnixNano())
		return
	}
	mathrand.Seed(n.Int64())
}
