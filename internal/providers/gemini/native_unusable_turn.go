package gemini

import (
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// turnEnding is how one candidate of a turn ended, reduced to what decides
// whether a client can use it. A non-streamed candidate and a streamed choice
// both reduce to one, so the two paths make the same decision.
type turnEnding struct {
	// finishReason is Gemini's finish reason as sent, such as STOP or
	// MALFORMED_FUNCTION_CALL; empty when the candidate never reported one.
	finishReason string
	// finishMessage is Gemini's prose about the finish. For a malformed call
	// it holds the call's text, which is model output.
	finishMessage string
	// hasOutput reports whether any of the candidate's parts is output (see
	// geminiPartHasOutput).
	hasOutput bool
}

// geminiPartHasOutput reports whether part reaches the client as output:
// text that isn't a thought, a function call, or a generated image. Thoughts
// and signature-only parts are dropped when a turn is rendered, so a turn
// made only of them gives the client nothing.
func geminiPartHasOutput(part geminiPart) bool {
	if part.Text != "" && !part.Thought {
		return true
	}
	if part.functionCall() != nil {
		return true
	}
	_, isImage := generatedImage(part)
	return isImage
}

// unusableTurnError reports a turn in which no candidate carries anything a
// client can use, as a 502 so the client retries. A candidate is unusable
// when it ended with MALFORMED_FUNCTION_CALL or MALFORMED_RESPONSE, where the
// model produced output Gemini could not encode (usually a broken tool call),
// or with STOP and neither output nor output tokens. Passed through, these
// read as a successful turn, and an agent client such as Claude Code ends
// its turn mid-task.
//
// Requires: outputTokens is the turn's candidatesTokenCount.
// Ensures:  nil when endings is empty or any ending is usable; otherwise an
// error naming the last ending's reason, with at most maxFailureTextLen bytes
// of its finish message.
func unusableTurnError(endings []turnEnding, outputTokens int, providerName string) *core.GatewayError {
	if len(endings) == 0 {
		return nil
	}
	var reason string
	for _, ending := range endings {
		reason = ending.unusableReason(outputTokens)
		if reason == "" {
			return nil
		}
	}
	return nativeProviderError(providerName, "Gemini returned no usable output: "+reason, nil)
}

// unusableReason returns why the ending leaves nothing a client can use, or
// "" when it doesn't.
func (e turnEnding) unusableReason(outputTokens int) string {
	reason := strings.ToUpper(strings.TrimSpace(e.finishReason))
	switch {
	case reason == "MALFORMED_FUNCTION_CALL", reason == "MALFORMED_RESPONSE":
	case reason == "STOP" && !e.hasOutput && outputTokens == 0:
		reason = "STOP with no output"
	default:
		return ""
	}
	if message := strings.TrimSpace(e.finishMessage); message != "" {
		return reason + ": " + truncateFailureText(message)
	}
	return reason
}

// turnEndings returns how each candidate of a response ended.
func (resp *geminiGenerateContentResponse) turnEndings() []turnEnding {
	endings := make([]turnEnding, 0, len(resp.Candidates))
	for _, candidate := range resp.Candidates {
		endings = append(endings, turnEnding{
			finishReason:  candidate.FinishReason,
			finishMessage: candidate.FinishMessage,
			hasOutput:     slices.ContainsFunc(candidate.Content.Parts, geminiPartHasOutput),
		})
	}
	return endings
}
