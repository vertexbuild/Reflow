// Example: Streaming Access Log Analyzer
//
// Pipeline: stream lines → parse → classify → filter → summarize
//
// Demonstrates:
//   - StreamNode for line-by-line processing of a log
//   - Per-item Settle that classifies, filters noise, and annotates events
//   - Collect to bridge streaming back to batch for summary
//   - Full hint and trace accumulation across stream → batch boundary
//
// This is a pure deterministic pipeline — no LLM required.
//
// Run with: go run ./examples/log_stream/
package main

import (
	"context"
	"fmt"
	"iter"
	"regexp"
	"strconv"
	"strings"

	"github.com/vertexbuild/reflow"
)

// --- Types ---

type LogEntry struct {
	IP     string
	Method string
	Path   string
	Status int
	Raw    string
}

type Event struct {
	Entry    LogEntry
	Class    string // "normal", "suspicious", "attack", "error"
	Severity int    // 0=info, 1=warning, 2=critical
	Reason   string
}

type Summary struct {
	Total      int
	Dropped    int
	Events     []Event
	AttackIPs  map[string]int
	ErrorPaths map[string]int
}

// --- Stream Node: Parse + Classify ---
// Streams raw log text line by line. Each line is parsed, classified,
// and settled independently. Normal traffic is dropped by settle.

type AnalyzeLog struct{}

func (AnalyzeLog) Resolve(_ context.Context, in reflow.Envelope[string]) (reflow.Envelope[string], error) {
	return in, nil
}

var logPattern = regexp.MustCompile(
	`^(\S+)\s+\S+\s+\S+\s+\[.*?\]\s+"(\w+)\s+(\S+)\s+\S+"\s+(\d+)\s+(\d+)`,
)

func (AnalyzeLog) Act(_ context.Context, in reflow.Envelope[string]) iter.Seq2[reflow.Envelope[Event], error] {
	return func(yield func(reflow.Envelope[Event], error) bool) {
		for _, line := range strings.Split(in.Value, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			m := logPattern.FindStringSubmatch(line)
			if m == nil {
				env := reflow.Envelope[Event]{
					Value: Event{Entry: LogEntry{Raw: line}},
					Meta:  in.Meta,
				}
				env = env.WithHint("parse.failed", "could not parse log line", line)
				if !yield(env, nil) {
					return
				}
				continue
			}

			entry := LogEntry{
				IP:     m[1],
				Method: m[2],
				Path:   m[3],
				Raw:    line,
			}
			entry.Status, _ = strconv.Atoi(m[4])

			event := classify(entry)
			env := reflow.Envelope[Event]{Value: event, Meta: in.Meta}

			if event.Severity > 0 {
				env = env.WithHint(
					fmt.Sprintf("event.%s", event.Class),
					event.Reason,
					fmt.Sprintf("%s %s → %d", entry.Method, entry.Path, entry.Status),
				)
			}

			if !yield(env, nil) {
				return
			}
		}
	}
}

// Settle filters: only events with severity > 0 pass through.
// Normal traffic is silently dropped.
func (AnalyzeLog) Settle(_ context.Context, _ reflow.Envelope[string], out reflow.Envelope[Event], actErr error) (reflow.Envelope[Event], bool, error) {
	if actErr != nil {
		return out, false, nil
	}
	if out.Value.Entry.IP == "" {
		return out, false, nil // unparseable
	}
	if out.Value.Severity == 0 {
		return out, false, nil // normal traffic
	}
	return out, true, nil
}

func classify(entry LogEntry) Event {
	event := Event{Entry: entry, Class: "normal", Severity: 0}

	switch {
	case strings.Contains(entry.Path, "'") ||
		strings.Contains(strings.ToLower(entry.Path), "union+select") ||
		strings.Contains(strings.ToLower(entry.Path), "drop+table"):
		event.Class = "attack"
		event.Severity = 2
		event.Reason = "SQL injection attempt"

	case strings.Contains(entry.Path, ".."):
		event.Class = "attack"
		event.Severity = 2
		event.Reason = "path traversal attempt"

	case strings.Contains(entry.Path, "/wp-admin") ||
		strings.Contains(entry.Path, "/phpmyadmin") ||
		strings.Contains(entry.Path, "/.env") ||
		strings.Contains(entry.Path, "/actuator"):
		event.Class = "suspicious"
		event.Severity = 1
		event.Reason = "reconnaissance scan"

	case entry.Status >= 500:
		event.Class = "error"
		event.Severity = 1
		event.Reason = fmt.Sprintf("server error %d", entry.Status)

	case entry.Status == 403:
		event.Class = "suspicious"
		event.Severity = 1
		event.Reason = "forbidden access attempt"
	}

	return event
}

// --- Main ---

const sampleLog = `192.168.1.100 - - [27/Mar/2026:10:15:01 +0000] "GET /index.html HTTP/1.1" 200 1234
192.168.1.100 - - [27/Mar/2026:10:15:02 +0000] "GET /api/users HTTP/1.1" 200 892
10.0.0.55 - - [27/Mar/2026:10:15:03 +0000] "GET /wp-admin/login.php HTTP/1.1" 404 0
10.0.0.55 - - [27/Mar/2026:10:15:04 +0000] "GET /phpmyadmin/ HTTP/1.1" 404 0
10.0.0.55 - - [27/Mar/2026:10:15:05 +0000] "GET /.env HTTP/1.1" 403 0
203.0.113.42 - - [27/Mar/2026:10:15:06 +0000] "GET /api/users?id=1'+OR+'1'='1 HTTP/1.1" 400 0
203.0.113.42 - - [27/Mar/2026:10:15:07 +0000] "GET /../../etc/passwd HTTP/1.1" 400 0
192.168.1.101 - - [27/Mar/2026:10:15:08 +0000] "POST /api/orders HTTP/1.1" 201 445
192.168.1.101 - - [27/Mar/2026:10:15:09 +0000] "GET /api/orders/42 HTTP/1.1" 200 312
172.16.0.99 - - [27/Mar/2026:10:15:10 +0000] "GET /api/health HTTP/1.1" 500 89
172.16.0.99 - - [27/Mar/2026:10:15:11 +0000] "GET /api/health HTTP/1.1" 503 89
192.168.1.100 - - [27/Mar/2026:10:15:12 +0000] "GET /static/style.css HTTP/1.1" 200 4521
10.0.0.55 - - [27/Mar/2026:10:15:13 +0000] "GET /actuator/env HTTP/1.1" 404 0
203.0.113.42 - - [27/Mar/2026:10:15:14 +0000] "POST /api/login HTTP/1.1" 403 0
192.168.1.102 - - [27/Mar/2026:10:15:15 +0000] "GET /api/products HTTP/1.1" 200 2891`

func main() {
	ctx := context.Background()

	totalLines := len(strings.Split(strings.TrimSpace(sampleLog), "\n"))

	// Stream: parse + classify + filter (settle drops normal traffic)
	events, err := reflow.Collect(reflow.Stream(ctx, AnalyzeLog{}, reflow.NewEnvelope(sampleLog)))
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	dropped := totalLines - len(events)

	fmt.Printf("Access Log Analyzer\n")
	fmt.Printf("==================================================\n\n")
	fmt.Printf("Streamed %d lines → %d events (%d normal traffic dropped)\n\n", totalLines, len(events), dropped)

	// Group by severity
	for _, sev := range []struct {
		level int
		label string
	}{{2, "CRITICAL"}, {1, "WARNING"}} {
		var matches []Event
		for _, e := range events {
			if e.Value.Severity == sev.level {
				matches = append(matches, e.Value)
			}
		}
		if len(matches) == 0 {
			continue
		}
		fmt.Printf("  %s:\n", sev.label)
		for _, e := range matches {
			fmt.Printf("    %-15s %-7s %-40s → %d  (%s)\n",
				e.Entry.IP, e.Entry.Method, truncate(e.Entry.Path, 40), e.Entry.Status, e.Reason)
		}
		fmt.Println()
	}

	// Suspicious IPs
	ips := make(map[string]int)
	for _, e := range events {
		if e.Value.Class == "attack" || e.Value.Class == "suspicious" {
			ips[e.Value.Entry.IP]++
		}
	}
	if len(ips) > 0 {
		fmt.Printf("  Suspicious IPs:\n")
		for ip, count := range ips {
			fmt.Printf("    %-15s  %d events\n", ip, count)
		}
		fmt.Println()
	}

	// Accumulated hints from the stream
	hints := 0
	traces := 0
	for _, e := range events {
		hints += len(e.Meta.Hints)
		traces += len(e.Meta.Trace)
	}
	fmt.Printf("Pipeline: %d hints across %d events | %d trace steps\n", hints, len(events), traces)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
