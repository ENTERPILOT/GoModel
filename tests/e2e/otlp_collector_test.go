//go:build e2e

package e2e

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	collectormetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// otlpTestCollector is an in-memory OTLP/HTTP receiver for traces and metrics.
type otlpTestCollector struct {
	server *httptest.Server

	mu      sync.Mutex
	traces  []*tracepb.ResourceSpans
	metrics []*metricpb.ResourceMetrics
}

func newOTLPTestCollector() *otlpTestCollector {
	collector := &otlpTestCollector{}
	collector.server = httptest.NewServer(http.HandlerFunc(collector.serveHTTP))
	return collector
}

func (c *otlpTestCollector) URL() string { return c.server.URL }

func (c *otlpTestCollector) Close() { c.server.Close() }

func (c *otlpTestCollector) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := readOTLPBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch r.URL.Path {
	case "/v1/traces":
		var request collectortracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		c.traces = append(c.traces, request.ResourceSpans...)
		c.mu.Unlock()
	case "/v1/metrics":
		var request collectormetricpb.ExportMetricsServiceRequest
		if err := proto.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		c.metrics = append(c.metrics, request.ResourceMetrics...)
		c.mu.Unlock()
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

func readOTLPBody(r *http.Request) ([]byte, error) {
	var reader io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		compressed, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer compressed.Close()
		reader = compressed
	}
	return io.ReadAll(reader)
}

func (c *otlpTestCollector) waitFor(timeout time.Duration, predicate func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return predicate()
}

func (c *otlpTestCollector) findSpan(predicate func(*tracepb.Span) bool) *tracepb.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, resource := range c.traces {
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				if predicate(span) {
					return proto.Clone(span).(*tracepb.Span)
				}
			}
		}
	}
	return nil
}

func (c *otlpTestCollector) hasHistogramPoint(name string, expected map[string]any) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, resource := range c.metrics {
		for _, scope := range resource.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if metric.Name != name || metric.GetHistogram() == nil {
					continue
				}
				for _, point := range metric.GetHistogram().DataPoints {
					if attributesContain(point.Attributes, expected) {
						return true
					}
				}
			}
		}
	}
	return false
}

// containsString reports whether value appears anywhere in exported resource
// attributes, span names and attributes, or metric names and attributes.
func (c *otlpTestCollector) containsString(value string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, resource := range c.traces {
		if keyValuesContainString(resource.Resource.GetAttributes(), value) {
			return true
		}
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				if strings.Contains(span.Name, value) || keyValuesContainString(span.Attributes, value) {
					return true
				}
			}
		}
	}
	for _, resource := range c.metrics {
		if keyValuesContainString(resource.Resource.GetAttributes(), value) {
			return true
		}
		for _, scope := range resource.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if strings.Contains(metric.Name, value) || metricContainsString(metric, value) {
					return true
				}
			}
		}
	}
	return false
}

func metricContainsString(metric *metricpb.Metric, value string) bool {
	if histogram := metric.GetHistogram(); histogram != nil {
		for _, point := range histogram.DataPoints {
			if keyValuesContainString(point.Attributes, value) {
				return true
			}
		}
	}
	if sum := metric.GetSum(); sum != nil {
		for _, point := range sum.DataPoints {
			if keyValuesContainString(point.Attributes, value) {
				return true
			}
		}
	}
	return false
}

func keyValuesContainString(attributes []*commonpb.KeyValue, value string) bool {
	for _, attribute := range attributes {
		if strings.Contains(attribute.Key, value) || strings.Contains(attribute.Value.GetStringValue(), value) {
			return true
		}
	}
	return false
}

func attributesContain(attributes []*commonpb.KeyValue, expected map[string]any) bool {
	actual := make(map[string]any, len(attributes))
	for _, attribute := range attributes {
		switch value := attribute.Value.Value.(type) {
		case *commonpb.AnyValue_StringValue:
			actual[attribute.Key] = value.StringValue
		case *commonpb.AnyValue_BoolValue:
			actual[attribute.Key] = value.BoolValue
		case *commonpb.AnyValue_IntValue:
			actual[attribute.Key] = value.IntValue
		}
	}
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}
