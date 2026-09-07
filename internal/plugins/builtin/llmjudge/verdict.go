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

// verdictSep matches what a model wraps a bare verdict in: whitespace,
// punctuation and symbols (quotes, code fences, braces, a colon after a
// label). Letters of any script are not separators, so a verdict glued to
// prose in another language ("allowこれは") is not accepted.
const verdictSep = `[\s\p{P}\p{S}]*`

// bareVerdictRe accepts a reply that is nothing but the verdict word, with
// an optional "verdict" label and separators around it (`allow`,
// "Verdict: block.", `{"verdict": block}`). A verdict embedded in a sentence
// is not accepted: "I should not allow this" must not read as allow, and a
// JSON reply cut off inside its reason must not either.
var bareVerdictRe = regexp.MustCompile(`^` + verdictSep + `(?:verdict` + verdictSep + `)?(allow|block)` + verdictSep + `$`)

// parseVerdict reads the judge reply. It takes the first JSON object with a
// recognized "verdict" key, falling back to a reply that is a bare "allow"
// or "block". Anything else is unclear.
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
	if m := bareVerdictRe.FindStringSubmatch(strings.ToLower(reply)); m != nil {
		return verdict{Verdict: m[1], Reason: "judge reply says " + m[1]}
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
