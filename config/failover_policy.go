package config

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultFailoverRetryStatuses is the failover.retry_on_statuses default: rate
// limits and every server-side failure.
var DefaultFailoverRetryStatuses = []string{"429", "5xx"}

// DefaultFailoverRetryErrors is the failover.retry_on_errors default. The
// phrases cover a model that is gone or refused whatever status the provider
// chose, an aggregator provider relaying a failure of its own upstream behind
// a 4xx, and retired-model 404s that avoid the word "model". Plain endpoint
// 404s and validation errors match none of them.
var DefaultFailoverRetryErrors = []string{
	"model not found",
	"model does not exist",
	"model unsupported",
	"model unavailable",
	"model not available",
	"model deprecated",
	"model retired",
	"model disabled",
	"upstream failed",
	"upstream error",
	"upstream unavailable",
	"upstream timed out",
	"upstream timeout",
	"404 unsupported",
	"404 unavailable",
	"404 not available",
	"404 deprecated",
	"404 retired",
	"404 disabled",
}

// FailoverErrorPhrase is one parsed retry_on_errors entry. Every Word must
// appear in the error text and every status constraint must hold.
type FailoverErrorPhrase struct {
	// Words are the lower-cased text fragments the error must contain.
	Words []string
	// Statuses are HTTP statuses the error must carry; empty means any.
	Statuses map[int]bool
}

// parseFailoverRetryStatuses expands status entries into the set of matching
// HTTP status codes. An empty list yields the default.
func parseFailoverRetryStatuses(entries []string) (map[int]bool, error) {
	if len(entries) == 0 {
		entries = DefaultFailoverRetryStatuses
	}
	statuses := make(map[int]bool)
	for _, entry := range entries {
		codes, ok := expandStatusToken(entry)
		if !ok {
			return nil, fmt.Errorf("failover.retry_on_statuses: %q is not an HTTP status code or class such as 5xx", entry)
		}
		for _, code := range codes {
			statuses[code] = true
		}
	}
	return statuses, nil
}

// parseFailoverRetryErrors lower-cases each phrase into its word list, lifting
// numeric words into status constraints. An empty list yields the default.
func parseFailoverRetryErrors(entries []string) ([]FailoverErrorPhrase, error) {
	if len(entries) == 0 {
		entries = DefaultFailoverRetryErrors
	}
	phrases := make([]FailoverErrorPhrase, 0, len(entries))
	for _, entry := range entries {
		var phrase FailoverErrorPhrase
		for word := range strings.FieldsSeq(strings.ToLower(entry)) {
			if codes, ok := expandStatusToken(word); ok {
				if phrase.Statuses == nil {
					phrase.Statuses = make(map[int]bool, len(codes))
				}
				for _, code := range codes {
					phrase.Statuses[code] = true
				}
				continue
			}
			phrase.Words = append(phrase.Words, word)
		}
		if len(phrase.Words) == 0 {
			return nil, fmt.Errorf("failover.retry_on_errors: %q must contain at least one non-numeric word", entry)
		}
		phrases = append(phrases, phrase)
	}
	return phrases, nil
}

// expandStatusToken turns "503" into [503] and "5xx" into 500..599. It reports
// false for anything else.
func expandStatusToken(token string) ([]int, bool) {
	token = strings.ToLower(strings.TrimSpace(token))
	if len(token) != 3 {
		return nil, false
	}
	if strings.HasSuffix(token, "xx") {
		class := int(token[0] - '0')
		if class < 1 || class > 5 {
			return nil, false
		}
		codes := make([]int, 0, 100)
		for code := class * 100; code < (class+1)*100; code++ {
			codes = append(codes, code)
		}
		return codes, true
	}
	code, err := strconv.Atoi(token)
	if err != nil || code < 100 || code > 599 {
		return nil, false
	}
	return []int{code}, true
}
