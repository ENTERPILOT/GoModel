package usage

import (
	"io"

	"gomodel/internal/streaming"
)

// StreamUsageWrapper wraps an io.ReadCloser to capture usage data from SSE streams.
// It incrementally parses SSE events as they arrive (on each \n\n boundary),
// extracting and caching usage data immediately when found. This handles
// arbitrarily large events like the Responses API's response.completed which includes
// the full response object alongside usage data.
type StreamUsageWrapper struct {
	io.ReadCloser
}

// NewStreamUsageWrapper creates a wrapper around a stream to capture usage data.
// When the stream is closed, it logs the cached usage entry if one was found.
func NewStreamUsageWrapper(stream io.ReadCloser, logger LoggerInterface, model, provider, requestID, endpoint string, pricingResolver PricingResolver) *StreamUsageWrapper {
	observer := NewStreamUsageObserver(logger, model, provider, requestID, endpoint, pricingResolver)
	if observer == nil {
		return &StreamUsageWrapper{ReadCloser: stream}
	}
	return &StreamUsageWrapper{
		ReadCloser: streaming.NewObservedSSEStream(stream, observer),
	}
}

// WrapStreamForUsage wraps a stream with usage tracking if enabled.
// This is a convenience function for use in handlers.
func WrapStreamForUsage(stream io.ReadCloser, logger LoggerInterface, model, provider, requestID, endpoint string, pricingResolver PricingResolver) io.ReadCloser {
	if logger == nil || !logger.Config().Enabled {
		return stream
	}
	return NewStreamUsageWrapper(stream, logger, model, provider, requestID, endpoint, pricingResolver)
}
