package session

import (
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// NewDetectorFromConfig builds the request session detector from application
// config. Returns nil when session keeping is disabled, which downstream
// consumers treat as "no session".
func NewDetectorFromConfig(cfg config.SessionConfig) *Detector {
	if !cfg.Enabled {
		return nil
	}
	var rules []Rule
	if cfg.BuiltinRules {
		rules = BuiltinRules()
	}
	configured := make([]Rule, 0, len(cfg.Headers))
	for _, header := range cfg.Headers {
		configured = append(configured, Rule{
			Source:    SourceHeader,
			Header:    header.Header,
			Transform: header.Transform,
		})
	}
	return NewDetector(mergeHeaderRules(rules, configured), cfg.AutoDetect)
}

// mergeHeaderRules overlays configured header rules onto the base registry: a
// configured header replaces the built-in rule with the same name in place,
// new headers are appended.
func mergeHeaderRules(base, configured []Rule) []Rule {
	merged := base
	for _, rule := range configured {
		replaced := false
		for i := range merged {
			if merged[i].Source == SourceHeader && strings.EqualFold(merged[i].Header, rule.Header) {
				merged[i] = rule
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, rule)
		}
	}
	return merged
}
