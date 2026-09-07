package llmjudge

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Verdict values reported in Decision.Detail.
const (
	VerdictAllow   = "allow"
	VerdictBlock   = "block"
	VerdictUnclear = "unclear"
)

// maxReason bounds the reason stored in the audit detail.
const maxReason = 200

// verdict is the parsed judge reply.
type verdict struct {
	Verdict string
	Reason  string
}

var wordRe = regexp.MustCompile(`\b(allow|block)\b`)

// parseVerdict reads the judge reply. It takes the first JSON object with a
// recognized "verdict" key, falling back to a whole-word scan for "allow"
// or "block" when exactly one of them appears.
func parseVerdict(reply string) verdict {
	for idx := strings.Index(reply, "{"); idx >= 0; {
		var obj struct {
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		}
		if err := json.NewDecoder(strings.NewReader(reply[idx:])).Decode(&obj); err == nil {
			switch strings.ToLower(strings.TrimSpace(obj.Verdict)) {
			case VerdictAllow:
				return verdict{Verdict: VerdictAllow, Reason: truncate(obj.Reason)}
			case VerdictBlock:
				return verdict{Verdict: VerdictBlock, Reason: truncate(obj.Reason)}
			}
		}
		next := strings.Index(reply[idx+1:], "{")
		if next < 0 {
			break
		}
		idx += 1 + next
	}
	allow, block := false, false
	for _, w := range wordRe.FindAllString(strings.ToLower(reply), -1) {
		allow = allow || w == VerdictAllow
		block = block || w == VerdictBlock
	}
	switch {
	case block && !allow:
		return verdict{Verdict: VerdictBlock, Reason: "judge reply says block"}
	case allow && !block:
		return verdict{Verdict: VerdictAllow, Reason: "judge reply says allow"}
	}
	return verdict{Verdict: VerdictUnclear, Reason: "judge reply could not be parsed"}
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxReason {
		return s
	}
	cut := maxReason
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
