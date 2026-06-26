// loadgen drives concurrent requests at one gateway endpoint and reports latency
// percentiles. It speaks three request dialects (OpenAI chat, OpenAI responses,
// Anthropic messages) in streaming and non-streaming modes, so a single binary
// covers all six benchmark variants.
//
// Two closed-loop modes:
//   - fixed count   (-n N):          send N requests at concurrency C, then stop.
//   - time-boxed    (-duration D):   keep C workers busy for D, counting
//                                    completions. Used by the capacity sweep to
//                                    measure *sustained* throughput at each
//                                    concurrency level (vs the latency-coupled
//                                    "completed req/s @ c=N" the fixed mode reports).
//
// For streaming requests it records TTFT (time to first token/byte) separately
// from total latency, plus inter-chunk gap percentiles (a pass-through gateway
// relays each upstream chunk immediately; a buffering one clumps them). Output is
// a JSON summary suitable for aggregation.
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

type headerList []string

func (h *headerList) String() string { return strings.Join(*h, ",") }
func (h *headerList) Set(v string) error {
	*h = append(*h, v)
	return nil
}

type result struct {
	ttft   time.Duration
	total  time.Duration
	gaps   []time.Duration // streaming: time between consecutive received chunks
	err    string
	capped bool // aborted by the variant wall cap — excluded from ok/failed
}

func main() {
	var (
		url      = flag.String("url", "", "Target URL")
		n        = flag.Int("n", 500, "Total requests (fixed-count mode; ignored when -duration > 0)")
		c        = flag.Int("c", 10, "Concurrency")
		duration = flag.Duration("duration", 0, "Time-boxed mode: keep -c workers busy for this long, count completions (capacity sweep)")
		dialect  = flag.String("dialect", "chat", "Request dialect: chat | responses | messages")
		stream   = flag.Bool("stream", false, "Stream the response")
		model    = flag.String("model", "gpt-4o-mini", "Model name")
		auth     = flag.String("auth", "sk-bench-test-key", "Bearer token for Authorization header")
		jsonOut  = flag.String("json", "", "Write JSON summary to this file")
		timeout  = flag.Duration("timeout", 30*time.Second, "Per-request hard timeout")
		idle     = flag.Duration("idle", 1500*time.Millisecond, "Streaming: end the stream if no new data arrives for this long")
		maxWall  = flag.Duration("max-wall", 0, "Fixed mode: stop launching new requests after this wall time (caps slow/idle-bound variants; 0 = no cap)")
	)
	var headers headerList
	flag.Var(&headers, "H", "Extra header 'Key: Value' (repeatable)")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "usage: loadgen -url <url> [-n] [-c] [-dialect chat|responses|messages] [-stream] [-model] [-H 'K: V']")
		os.Exit(2)
	}

	body := buildBody(*dialect, *model, *stream)
	// Tuned transport: keep a full set of hot keep-alive connections for the
	// configured concurrency so the measured window isn't paying TCP setup. The
	// stdlib default caps idle conns per host at 2, which would churn connections
	// at c>2 and add noise to short windows.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = *c * 2
	tr.MaxIdleConnsPerHost = *c * 2
	client := &http.Client{Transport: tr}

	do := func(ctx context.Context) result {
		return doRequest(ctx, client, *url, body, *dialect, *stream, *auth, headers, *timeout, *idle)
	}

	var results []result
	var wall time.Duration
	if *duration > 0 {
		results, wall = driveTimeBoxed(do, *c, *duration)
	} else {
		results, wall = driveFixed(do, *n, *c, *maxWall)
	}

	report(*url, *dialect, *stream, len(results), *c, wall, *duration, results, *jsonOut)
}

// driveFixed sends up to n requests at concurrency c (closed loop). If maxWall>0
// it caps the variant at that wall time: it stops launching new requests AND
// cancels any in-flight ones via the shared context, so an idle-bound streaming
// variant can't run for the full N at ~7 req/s (and can't stall the launch loop
// on a full semaphore either). Fast variants reach N long before the cap; slow
// ones return however many completed in the window. Requests aborted *by* the cap
// are tagged capped (excluded from ok/failed) rather than counted as errors.
func driveFixed(do func(context.Context) result, n, c int, maxWall time.Duration) ([]result, time.Duration) {
	capCtx := context.Background()
	if maxWall > 0 {
		var cancel context.CancelFunc
		capCtx, cancel = context.WithTimeout(capCtx, maxWall)
		defer cancel()
	}
	var mu sync.Mutex
	results := make([]result, 0, n)
	sem := make(chan struct{}, c)
	var wg sync.WaitGroup
	start := time.Now()
loop:
	for i := 0; i < n; i++ {
		select {
		case <-capCtx.Done(): // cap reached — stop launching even if the sem is full
			break loop
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := do(capCtx)
			if r.err != "" && capCtx.Err() != nil {
				r.capped = true // aborted by the cap, not a genuine failure
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results, time.Since(start)
}

// driveTimeBoxed keeps c workers continuously busy for d, returning every result
// completed in the window. This measures sustained throughput at concurrency c
// (the capacity sweep), independent of any fixed request count. The shared
// context cancels each worker's final in-flight request at the deadline so the
// tail can't overrun by a full per-request timeout.
func driveTimeBoxed(do func(context.Context) result, c int, d time.Duration) ([]result, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var mu sync.Mutex
	var results []result
	var wg sync.WaitGroup
	start := time.Now()
	for range c {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				r := do(ctx)
				if r.err != "" && ctx.Err() != nil {
					r.capped = true // tail request cut off at the window edge
				}
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return results, time.Since(start)
}

func buildBody(dialect, model string, stream bool) []byte {
	const prompt = "Say hello for a benchmark test."
	var req map[string]any
	switch dialect {
	case "responses":
		req = map[string]any{"model": model, "stream": stream, "input": prompt}
	case "messages":
		req = map[string]any{
			"model": model, "stream": stream, "max_tokens": 256,
			"messages": []map[string]any{{"role": "user", "content": prompt}},
		}
	default: // chat
		req = map[string]any{
			"model": model, "stream": stream,
			"messages": []map[string]any{{"role": "user", "content": prompt}},
		}
	}
	b, _ := json.Marshal(req)
	return b
}

func doRequest(parent context.Context, client *http.Client, url string, body []byte, dialect string, stream bool, auth string, headers headerList, timeout, idle time.Duration) result {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return result{err: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	if dialect == "messages" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	for _, h := range headers {
		k, v, ok := strings.Cut(h, ":")
		if ok {
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}

	startReq := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return result{err: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return result{err: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))}
	}

	if !stream {
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return result{err: "read body: " + err.Error()}
		}
		d := time.Since(startReq)
		return result{ttft: d, total: d}
	}
	return readStream(resp.Body, startReq, dialect, idle)
}

// readStream consumes an SSE body, recording TTFT at the first data line and
// total latency at the last received chunk. A stream ends when any of these
// occurs: the dialect's terminal marker is seen, the connection closes, or no
// new data arrives for `idle` (a fallback for gateways that stream content but
// never send a terminal event nor close — e.g. Bifrost's responses/messages
// streams over an OpenAI-backed provider). Total latency is always measured to
// the last byte, so the idle wait never inflates the reported latency.
func readStream(r io.Reader, startReq time.Time, dialect string, idle time.Duration) result {
	lines := make(chan string, 128)
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	var ttft, total, prev time.Duration
	var gaps []time.Duration
	gotFirst := false
	timer := time.NewTimer(idle)
	defer timer.Stop()

	for {
		select {
		case line, ok := <-lines:
			if !ok { // connection closed
				if !gotFirst {
					return result{err: "empty stream"}
				}
				return result{ttft: ttft, total: total, gaps: gaps}
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			now := time.Since(startReq)
			if !gotFirst {
				ttft = now
				gotFirst = true
			} else {
				gaps = append(gaps, now-prev) // time since the previous chunk
			}
			prev = now
			total = now // advance to the most recent chunk
			if isTerminal(dialect, line[len("data: "):]) {
				return result{ttft: ttft, total: total, gaps: gaps}
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		case <-timer.C: // idle gap: treat as end-of-stream at the last chunk
			if !gotFirst {
				return result{err: "no data before idle timeout"}
			}
			return result{ttft: ttft, total: total, gaps: gaps}
		}
	}
}

// isTerminal recognizes each dialect's end-of-stream markers, including the
// content-complete events some gateways send instead of a final wrapper event.
func isTerminal(dialect, payload string) bool {
	switch dialect {
	case "responses":
		return strings.Contains(payload, `"response.completed"`) ||
			strings.Contains(payload, `"response.output_text.done"`)
	case "messages":
		return strings.Contains(payload, `"message_stop"`)
	default: // chat
		return payload == "[DONE]"
	}
}

func report(url, dialect string, stream bool, n, c int, wall, duration time.Duration, results []result, jsonOut string) {
	var ttfts, totals, gaps []float64
	errs := map[string]int{}
	errCount := 0
	cappedCount := 0
	for _, r := range results {
		if r.capped { // cut off by the wall cap — neither a success nor a failure
			cappedCount++
			continue
		}
		if r.err != "" {
			errCount++
			errs[r.err]++
			continue
		}
		ttfts = append(ttfts, float64(r.ttft.Microseconds()))
		totals = append(totals, float64(r.total.Microseconds()))
		for _, g := range r.gaps {
			gaps = append(gaps, float64(g.Microseconds()))
		}
	}
	ok := len(totals)
	sort.Float64s(ttfts)
	sort.Float64s(totals)
	sort.Float64s(gaps)

	mode := "nonstream"
	if stream {
		mode = "stream"
	}
	rps := 0.0
	if wall > 0 {
		rps = float64(ok) / wall.Seconds()
	}
	// measure documents how rps should be read: throughput = sustained capacity
	// at concurrency c; latency = completed req/s @ c (coupled to per-req latency).
	measure := "latency"
	if duration > 0 {
		measure = "throughput"
	}

	// "-" means machine-readable only: emit JSON to stdout, skip the human report.
	if jsonOut == "-" {
		writeSummary("-", url, dialect, mode, measure, n, ok, errCount, cappedCount, c, wall, rps, ttfts, totals, gaps, errs)
		return
	}

	fmt.Printf("\n=== %s/%s  %s  (%s) ===\n", dialect, mode, url, measure)
	fmt.Printf("requests: %d  ok: %d  failed: %d  capped: %d  concurrency: %d  wall: %s  rps: %.1f\n",
		n, ok, errCount, cappedCount, c, wall.Round(time.Millisecond), rps)
	if ok > 0 {
		fmt.Printf("total latency ms  p50=%.2f p90=%.2f p95=%.2f p99=%.2f max=%.2f\n",
			ms(pct(totals, 50)), ms(pct(totals, 90)), ms(pct(totals, 95)), ms(pct(totals, 99)), ms(totals[ok-1]))
		if stream {
			fmt.Printf("ttft ms           p50=%.2f p90=%.2f p95=%.2f p99=%.2f\n",
				ms(pct(ttfts, 50)), ms(pct(ttfts, 90)), ms(pct(ttfts, 95)), ms(pct(ttfts, 99)))
			if len(gaps) > 0 {
				fmt.Printf("inter-chunk ms    p50=%.2f p90=%.2f p99=%.2f\n",
					ms(pct(gaps, 50)), ms(pct(gaps, 90)), ms(pct(gaps, 99)))
			}
		}
	}
	for e, ct := range errs {
		fmt.Printf("  error x%d: %s\n", ct, e)
	}

	if jsonOut != "" {
		writeSummary(jsonOut, url, dialect, mode, measure, n, ok, errCount, cappedCount, c, wall, rps, ttfts, totals, gaps, errs)
	}
}

func writeSummary(path, url, dialect, mode, measure string, n, ok, errCount, cappedCount, c int, wall time.Duration, rps float64, ttfts, totals, gaps []float64, errs map[string]int) {
	sample := func(s []float64) map[string]any {
		if len(s) == 0 {
			return map[string]any{}
		}
		return map[string]any{
			"p50_ms": ms(pct(s, 50)), "p90_ms": ms(pct(s, 90)),
			"p95_ms": ms(pct(s, 95)), "p99_ms": ms(pct(s, 99)),
			"min_ms": ms(s[0]), "max_ms": ms(s[len(s)-1]), "avg_ms": ms(avg(s)),
		}
	}
	out := map[string]any{
		"url": url, "dialect": dialect, "mode": mode, "measure": measure,
		"requests": n, "ok": ok, "failed": errCount, "capped": cappedCount, "concurrency": c,
		"wall_ms": wall.Milliseconds(), "rps": rps,
		"total_latency": sample(totals),
		"ttft":          sample(ttfts),
		"inter_chunk":   sample(gaps),
		"errors":        errs,
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if path == "-" {
		fmt.Println(string(b))
		return
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
		os.Exit(1)
	}
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := p / 100 * float64(len(sorted)-1)
	lo, hi := int(math.Floor(idx)), int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

func avg(s []float64) float64 {
	sum := 0.0
	for _, v := range s {
		sum += v
	}
	return sum / float64(len(s))
}

func ms(us float64) float64 { return math.Round(us/10) / 100 } // microseconds -> ms, 2dp
