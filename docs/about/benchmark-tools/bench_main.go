package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatResponse struct {
	Usage usage `json:"usage"`
}

type errorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type requestResult struct {
	LatencyMS        float64 `json:"latency_ms"`
	StatusCode       int     `json:"status_code"`
	Error            string  `json:"error,omitempty"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
}

type processSample struct {
	CPUPercent float64 `json:"cpu_percent"`
	RSSMB      float64 `json:"rss_mb"`
}

type summary struct {
	Timestamp   string  `json:"timestamp"`
	Gateway     string  `json:"gateway"`
	BaseURL     string  `json:"base_url"`
	Model       string  `json:"model"`
	Requests    int     `json:"requests"`
	Concurrency int     `json:"concurrency"`
	DurationSec float64 `json:"duration_sec"`

	Success   int     `json:"success"`
	Failures  int     `json:"failures"`
	ErrorRate float64 `json:"error_rate"`
	ReqPerSec float64 `json:"req_per_sec"`

	LatencyMS struct {
		Min float64 `json:"min"`
		P50 float64 `json:"p50"`
		P90 float64 `json:"p90"`
		P95 float64 `json:"p95"`
		P99 float64 `json:"p99"`
		Max float64 `json:"max"`
		Avg float64 `json:"avg"`
	} `json:"latency_ms"`

	Tokens struct {
		Prompt             int     `json:"prompt"`
		Completion         int     `json:"completion"`
		Total              int     `json:"total"`
		AvgTotalPerRequest float64 `json:"avg_total_per_request"`
		CompletionTPS      float64 `json:"completion_tps"`
	} `json:"tokens"`

	Process struct {
		Samples  int     `json:"samples"`
		CPUAvg   float64 `json:"cpu_avg"`
		CPUMax   float64 `json:"cpu_max"`
		RSSAvgMB float64 `json:"rss_avg_mb"`
		RSSMaxMB float64 `json:"rss_max_mb"`
	} `json:"process"`

	ErrorBreakdown map[string]int `json:"error_breakdown"`
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	pos := (p / 100) * float64(len(sorted)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower == upper {
		return sorted[lower]
	}
	weight := pos - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func sampleProcess(pid int) (processSample, error) {
	cmd := exec.Command("ps", "-o", "%cpu=,rss=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil {
		return processSample{}, err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return processSample{}, fmt.Errorf("no process sample")
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return processSample{}, fmt.Errorf("unexpected ps output: %q", line)
	}
	cpu, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return processSample{}, err
	}
	rssKB, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return processSample{}, err
	}
	return processSample{
		CPUPercent: cpu,
		RSSMB:      rssKB / 1024.0,
	}, nil
}

func main() {
	var (
		gateway        = flag.String("gateway", "gateway", "Gateway name label")
		baseURL        = flag.String("base-url", "http://127.0.0.1:8080", "Gateway base URL")
		model          = flag.String("model", "", "Model name")
		requests       = flag.Int("requests", 100, "Total number of requests")
		concurrency    = flag.Int("concurrency", 4, "Number of workers")
		prompt         = flag.String("prompt", "Reply with: OK", "User prompt")
		maxTokens      = flag.Int("max-tokens", 8, "max_tokens")
		temperature    = flag.Float64("temperature", 0, "temperature")
		requestTimeout = flag.Duration("request-timeout", 45*time.Second, "Per-request timeout")
		outputFile     = flag.String("output", "", "Output JSON file path")
		apiKey         = flag.String("api-key", "", "Optional bearer token for Authorization header")
		pid            = flag.Int("pid", 0, "Optional PID of gateway process to sample")
		sampleEvery    = flag.Duration("sample-every", 500*time.Millisecond, "Process sample interval")
	)
	flag.Parse()

	if *model == "" {
		fmt.Fprintln(os.Stderr, "missing required -model")
		os.Exit(1)
	}
	if *requests <= 0 || *concurrency <= 0 {
		fmt.Fprintln(os.Stderr, "-requests and -concurrency must be > 0")
		os.Exit(1)
	}

	type payloadType struct {
		Model       string  `json:"model"`
		Messages    []any   `json:"messages"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature"`
	}

	payload := payloadType{
		Model: *model,
		Messages: []any{
			map[string]string{"role": "user", "content": *prompt},
		},
		MaxTokens:   *maxTokens,
		Temperature: *temperature,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal payload: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        512,
			MaxIdleConnsPerHost: 512,
			MaxConnsPerHost:     512,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	var (
		resultsMu sync.Mutex
		results   = make([]requestResult, 0, *requests)

		procMu      sync.Mutex
		procSamples = make([]processSample, 0, 128)
	)

	stopSampling := make(chan struct{})
	if *pid > 0 {
		go func() {
			if sample, sampleErr := sampleProcess(*pid); sampleErr == nil {
				procMu.Lock()
				procSamples = append(procSamples, sample)
				procMu.Unlock()
			}
			ticker := time.NewTicker(*sampleEvery)
			defer ticker.Stop()
			for {
				select {
				case <-stopSampling:
					return
				case <-ticker.C:
					sample, sampleErr := sampleProcess(*pid)
					if sampleErr != nil {
						continue
					}
					procMu.Lock()
					procSamples = append(procSamples, sample)
					procMu.Unlock()
				}
			}
		}()
	}

	jobs := make(chan int, *requests)
	var wg sync.WaitGroup

	start := time.Now()
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				reqCtx, cancel := context.WithTimeout(context.Background(), *requestTimeout)
				req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(*baseURL, "/")+"/v1/chat/completions", bytes.NewReader(bodyBytes))
				if reqErr != nil {
					cancel()
					resultsMu.Lock()
					results = append(results, requestResult{Error: reqErr.Error()})
					resultsMu.Unlock()
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				if *apiKey != "" {
					req.Header.Set("Authorization", "Bearer "+*apiKey)
				}

				reqStart := time.Now()
				resp, doErr := client.Do(req)
				latencyMS := float64(time.Since(reqStart).Microseconds()) / 1000.0
				if doErr != nil {
					cancel()
					resultsMu.Lock()
					results = append(results, requestResult{
						LatencyMS:  latencyMS,
						StatusCode: 0,
						Error:      doErr.Error(),
					})
					resultsMu.Unlock()
					continue
				}

				respBody, _ := ioReadAllLimit(resp.Body, 1<<20)
				cancel()

				r := requestResult{
					LatencyMS:  latencyMS,
					StatusCode: resp.StatusCode,
				}

				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					var parsed chatResponse
					if unmarshalErr := json.Unmarshal(respBody, &parsed); unmarshalErr == nil {
						r.PromptTokens = parsed.Usage.PromptTokens
						r.CompletionTokens = parsed.Usage.CompletionTokens
						r.TotalTokens = parsed.Usage.TotalTokens
					}
				} else {
					errLabel := fmt.Sprintf("status_%d", resp.StatusCode)
					var er errorResponse
					if unmarshalErr := json.Unmarshal(respBody, &er); unmarshalErr == nil {
						msg := strings.TrimSpace(er.Error.Message)
						if msg != "" {
							errLabel = errLabel + ": " + msg
						}
					}
					r.Error = errLabel
				}

				resultsMu.Lock()
				results = append(results, r)
				resultsMu.Unlock()
			}
		}()
	}

	for i := 0; i < *requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	duration := time.Since(start)
	close(stopSampling)

	s := summary{
		Timestamp:      time.Now().Format(time.RFC3339),
		Gateway:        *gateway,
		BaseURL:        *baseURL,
		Model:          *model,
		Requests:       *requests,
		Concurrency:    *concurrency,
		DurationSec:    duration.Seconds(),
		ErrorBreakdown: map[string]int{},
	}

	latencies := make([]float64, 0, len(results))
	var (
		totalLatencyMS     float64
		totalPromptTokens  int
		totalCompTokens    int
		totalAllTokens     int
		successfulRequests int
	)
	for _, r := range results {
		if r.StatusCode >= 200 && r.StatusCode < 300 {
			successfulRequests++
			latencies = append(latencies, r.LatencyMS)
			totalLatencyMS += r.LatencyMS
			totalPromptTokens += r.PromptTokens
			totalCompTokens += r.CompletionTokens
			totalAllTokens += r.TotalTokens
		} else {
			key := r.Error
			if key == "" {
				key = fmt.Sprintf("status_%d", r.StatusCode)
			}
			s.ErrorBreakdown[key]++
		}
	}

	s.Success = successfulRequests
	s.Failures = s.Requests - s.Success
	if s.Requests > 0 {
		s.ErrorRate = float64(s.Failures) / float64(s.Requests)
		s.ReqPerSec = float64(s.Success) / s.DurationSec
	}

	sort.Float64s(latencies)
	if len(latencies) > 0 {
		s.LatencyMS.Min = latencies[0]
		s.LatencyMS.P50 = percentile(latencies, 50)
		s.LatencyMS.P90 = percentile(latencies, 90)
		s.LatencyMS.P95 = percentile(latencies, 95)
		s.LatencyMS.P99 = percentile(latencies, 99)
		s.LatencyMS.Max = latencies[len(latencies)-1]
		s.LatencyMS.Avg = totalLatencyMS / float64(len(latencies))
	}

	s.Tokens.Prompt = totalPromptTokens
	s.Tokens.Completion = totalCompTokens
	s.Tokens.Total = totalAllTokens
	if s.Success > 0 {
		s.Tokens.AvgTotalPerRequest = float64(totalAllTokens) / float64(s.Success)
	}
	if s.DurationSec > 0 {
		s.Tokens.CompletionTPS = float64(totalCompTokens) / s.DurationSec
	}

	procMu.Lock()
	defer procMu.Unlock()
	s.Process.Samples = len(procSamples)
	if len(procSamples) > 0 {
		var cpuSum, rssSum float64
		for _, sample := range procSamples {
			cpuSum += sample.CPUPercent
			rssSum += sample.RSSMB
			if sample.CPUPercent > s.Process.CPUMax {
				s.Process.CPUMax = sample.CPUPercent
			}
			if sample.RSSMB > s.Process.RSSMaxMB {
				s.Process.RSSMaxMB = sample.RSSMB
			}
		}
		s.Process.CPUAvg = cpuSum / float64(len(procSamples))
		s.Process.RSSAvgMB = rssSum / float64(len(procSamples))
	}

	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal summary: %v\n", err)
		os.Exit(1)
	}
	if *outputFile != "" {
		if err := os.WriteFile(*outputFile, out, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write output: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Println(string(out))
}

func ioReadAllLimit(body io.ReadCloser, max int64) ([]byte, error) {
	defer func() { _ = body.Close() }()
	limited := &io.LimitedReader{R: body, N: max}
	return io.ReadAll(limited)
}
