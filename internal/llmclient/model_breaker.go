package llmclient

import (
	"crypto/sha256"
	"time"
)

const maxModelBreakers = 1024
const modelBreakerIdleTTL = 10 * time.Minute

type modelBreakerEntry struct {
	breaker  *circuitBreaker
	active   int
	lastUsed time.Time
}

// breakerForModel pins a breaker until finishRequest releases it. Only idle,
// closed breakers can be evicted, preserving failures and in-flight probes.
// Fixed-size hashes also bound retained memory for caller-supplied model names.
func (c *Client) breakerForModel(model string) *circuitBreaker {
	if c.circuitBreaker == nil || c.config.CircuitBreaker.Scope != "model" || model == "" || model == UnknownModel {
		return c.circuitBreaker
	}
	key := sha256.Sum256([]byte(model))
	c.modelBreakersMu.Lock()
	defer c.modelBreakersMu.Unlock()
	if entry := c.modelBreakers[key]; entry != nil {
		entry.active++
		return entry.breaker
	}
	now := time.Now()
	var oldestKey [32]byte
	var oldest *modelBreakerEntry
	for k, entry := range c.modelBreakers {
		if entry.active != 0 || entry.breaker.State() != "closed" {
			continue
		}
		if now.Sub(entry.lastUsed) >= modelBreakerIdleTTL {
			delete(c.modelBreakers, k)
			continue
		}
		if oldest == nil || entry.lastUsed.Before(oldest.lastUsed) {
			oldestKey, oldest = k, entry
		}
	}
	if len(c.modelBreakers) >= maxModelBreakers {
		if oldest == nil {
			return nil
		} // Fail fast rather than lose an active breaker.
		delete(c.modelBreakers, oldestKey)
	}
	if c.modelBreakers == nil {
		c.modelBreakers = make(map[[32]byte]*modelBreakerEntry)
	}
	cfg := c.config.CircuitBreaker
	breaker := newCircuitBreaker(cfg.FailureThreshold, cfg.SuccessThreshold, cfg.Timeout)
	c.modelBreakers[key] = &modelBreakerEntry{breaker: breaker, active: 1, lastUsed: now}
	return breaker
}

// releaseModelBreaker makes a completed request's idle closed entry evictable.
func (c *Client) releaseModelBreaker(model string, breaker *circuitBreaker) {
	if breaker == nil || breaker == c.circuitBreaker {
		return
	}
	key := sha256.Sum256([]byte(model))
	c.modelBreakersMu.Lock()
	defer c.modelBreakersMu.Unlock()
	if entry := c.modelBreakers[key]; entry != nil && entry.breaker == breaker {
		entry.active--
		entry.lastUsed = time.Now()
	}
}
