// Streaming SSE benchmark tool.
// Sends concurrent streaming requests and measures TTFB + total latency.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type result struct {
	TTFB     time.Duration
	Total    time.Duration
	Chunks   int
	HasError bool
	Error    string
}

func main() {
	url := flag.String("url", "", "Target URL")
	n := flag.Int("n", 200, "Total requests")
	c := flag.Int("c", 50, "Concurrency")
	endpoint := flag.String("endpoint", "chat", "Endpoint: chat or responses")
	model := flag.String("model", "gpt-4o-mini", "Model name to use in requests")
	jsonOut := flag.String("json", "", "Write JSON results to file")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "Usage: stream-bench -url <url> [-n 200] [-c 50] [-endpoint chat|responses] [-model gpt-4o-mini]")
		os.Exit(1)
	}

	body := buildRequestBody(*endpoint, *model)
	results := make([]result, *n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, *c)

	start := time.Now()
	for i := range *n {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = doStreamRequest(*url, body)
		}(i)
	}
	wg.Wait()
	wallTime := time.Since(start)

	// Collect stats
	var ttfbs, totals []float64
	errors := 0
	totalChunks := 0
	for _, r := range results {
		if r.HasError {
			errors++
			continue
		}
		ttfbs = append(ttfbs, float64(r.TTFB.Microseconds()))
		totals = append(totals, float64(r.Total.Microseconds()))
		totalChunks += r.Chunks
	}

	successful := len(ttfbs)
	if successful == 0 {
		fmt.Println("All requests failed!")
		for i, r := range results {
			if r.HasError && i < 5 {
				fmt.Printf("  Error %d: %s\n", i, r.Error)
			}
		}
		os.Exit(1)
	}

	sort.Float64s(ttfbs)
	sort.Float64s(totals)

	rps := float64(successful) / wallTime.Seconds()

	fmt.Printf("\n=== Streaming Benchmark Results ===\n")
	fmt.Printf("URL:            %s\n", *url)
	fmt.Printf("Endpoint:       %s\n", *endpoint)
	fmt.Printf("Requests:       %d total, %d successful, %d failed\n", *n, successful, errors)
	fmt.Printf("Concurrency:    %d\n", *c)
	fmt.Printf("Wall time:      %s\n", wallTime.Round(time.Millisecond))
	fmt.Printf("Throughput:     %.2f req/s\n\n", rps)

	fmt.Printf("  TTFB (time to first byte):\n")
	printPercentiles("    ", ttfbs)

	fmt.Printf("\n  Total latency:\n")
	printPercentiles("    ", totals)

	fmt.Printf("\n  Avg chunks/response: %d\n", totalChunks/successful)

	if *jsonOut != "" {
		writeJSON(*jsonOut, *endpoint, *n, *c, successful, errors, wallTime, rps, ttfbs, totals, totalChunks)
	}
}

func buildRequestBody(endpoint, model string) []byte {
	var req any
	if endpoint == "responses" {
		req = map[string]any{
			"model":  model,
			"stream": true,
			"input":  "Say hello for a benchmark test.",
		}
	} else {
		req = map[string]any{
			"model":  model,
			"stream": true,
			"messages": []map[string]any{
				{"role": "user", "content": "Say hello for a benchmark test."},
			},
		}
	}
	b, _ := json.Marshal(req)
	return b
}

func doStreamRequest(url string, body []byte) result {
	reqStart := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-bench-test-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return result{HasError: true, Error: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return result{HasError: true, Error: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(b[:min(len(b), 200)]))}
	}

	var ttfb time.Duration
	ttfbRecorded := false
	chunks := 0

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 6 && line[:6] == "data: " {
			if !ttfbRecorded {
				ttfb = time.Since(reqStart)
				ttfbRecorded = true
			}
			chunks++
			// Detect end-of-stream markers
			payload := line[6:]
			if payload == "[DONE]" {
				break
			}
			// For responses API: detect response.completed or empty-type final chunk
			if strings.Contains(payload, `"response.completed"`) ||
				strings.Contains(payload, `"response.output_text.done"`) {
				break
			}
		}
	}

	total := time.Since(reqStart)
	if !ttfbRecorded {
		ttfb = total
	}
	return result{TTFB: ttfb, Total: total, Chunks: chunks}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := p / 100 * float64(len(sorted)-1)
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper {
		return sorted[lower]
	}
	frac := idx - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}

func printPercentiles(prefix string, data []float64) {
	fmt.Printf("%sp50:  %s\n", prefix, fmtUs(percentile(data, 50)))
	fmt.Printf("%sp95:  %s\n", prefix, fmtUs(percentile(data, 95)))
	fmt.Printf("%sp99:  %s\n", prefix, fmtUs(percentile(data, 99)))
	fmt.Printf("%smin:  %s\n", prefix, fmtUs(data[0]))
	fmt.Printf("%smax:  %s\n", prefix, fmtUs(data[len(data)-1]))
	avg := 0.0
	for _, v := range data {
		avg += v
	}
	avg /= float64(len(data))
	fmt.Printf("%savg:  %s\n", prefix, fmtUs(avg))
}

func fmtUs(us float64) string {
	if us < 1000 {
		return fmt.Sprintf("%.0fus", us)
	}
	return fmt.Sprintf("%.2fms", us/1000)
}

func writeJSON(path, endpoint string, n, c, ok, errs int, wall time.Duration, rps float64, ttfbs, totals []float64, chunks int) {
	data := map[string]any{
		"endpoint":     endpoint,
		"requests":     n,
		"concurrency":  c,
		"successful":   ok,
		"errors":       errs,
		"wall_time_ms": wall.Milliseconds(),
		"rps":          rps,
		"ttfb": map[string]any{
			"p50_us": percentile(ttfbs, 50),
			"p95_us": percentile(ttfbs, 95),
			"p99_us": percentile(ttfbs, 99),
			"min_us": ttfbs[0],
			"max_us": ttfbs[len(ttfbs)-1],
		},
		"total_latency": map[string]any{
			"p50_us": percentile(totals, 50),
			"p95_us": percentile(totals, 95),
			"p99_us": percentile(totals, 99),
			"min_us": totals[0],
			"max_us": totals[len(totals)-1],
		},
		"avg_chunks": chunks / max(ok, 1),
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write JSON output %s: %v\n", path, err)
		os.Exit(1)
	}
}
