package llmclient

// Requests without a model (such as discovery) use the provider breaker.
func (c *Client) breakerForModel(model string) *circuitBreaker {
	if c.circuitBreaker == nil || c.config.CircuitBreaker.Scope != "model" || model == "" {
		return c.circuitBreaker
	}
	if breaker, ok := c.modelBreakers.Load(model); ok {
		return breaker.(*circuitBreaker)
	}
	cfg := c.config.CircuitBreaker
	breaker, _ := c.modelBreakers.LoadOrStore(model, newCircuitBreaker(cfg.FailureThreshold, cfg.SuccessThreshold, cfg.Timeout))
	return breaker.(*circuitBreaker)
}
