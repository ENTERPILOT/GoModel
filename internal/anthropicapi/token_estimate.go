package anthropicapi

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"  // register decoders so image dimensions can be read
	_ "image/jpeg" // from the encoded header without a full decode
	_ "image/png"
	"math"
	"strings"
	"unicode"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// Token estimation without a tokenizer. Clients call count_tokens to decide
// whether a request still fits the context window, so the estimate must not
// fall far short of the real count on ordinary traffic. A flat characters/4
// holds for English prose only: JSON, code, CJK, and emoji tokenize several
// times denser, and images cost by area. The weights below were calibrated
// against Anthropic's count_tokens on those inputs and land within about ten
// percent of it, except for raw base64 in text, which stays under-counted.
const (
	tokensPerLetter    = 0.25 // ASCII letters and whitespace: four characters per token
	tokensPerDigit     = 0.5  // numbers split every two to three digits
	tokensPerSymbol    = 0.75 // punctuation: one token in code and JSON, merged with a word in prose
	tokensPerOtherRune = 0.85 // CJK and other non-ASCII script: nearly one token per character
	tokensPerEmoji     = 2.7  // multi-codepoint emoji: two to four tokens each
	tokensPerBlobChar  = 0.6  // hashes, ids, and other long letter-digit runs
	blobMinLength      = 24

	messageOverhead        = 4   // role framing per message
	toolBlockOverhead      = 10  // tool_use / tool_result framing per block
	toolDefinitionOverhead = 25  // per tool definition, on top of its schema
	toolUseSystemPrompt    = 350 // Anthropic's tool-use system prompt when tools are present

	imageTokenDivisor = 750  // tokens = width × height / 750
	imageMaxTokens    = 1600 // Anthropic downscales images to ~1.15 megapixels
)

// EstimateInputTokens estimates the input token count of a Messages request.
// It is an approximation, not a tokenizer-exact count; the provider's own
// count is used instead whenever the route offers one.
func EstimateInputTokens(req *MessagesRequest) int {
	if req == nil {
		return 0
	}
	// Errors are ignored here: count_tokens is a best-effort heuristic and
	// must not fail on malformed sub-fields that ToChatRequest would reject.
	system, _ := systemText(req.System)
	total := textTokens(system)
	for _, msg := range req.Messages {
		total += messageOverhead
		text, blocks, err := parseContent(msg.Content)
		if err != nil {
			continue
		}
		total += textTokens(text)
		for _, block := range blocks {
			total += contentBlockTokens(block)
		}
	}
	if len(req.Tools) > 0 {
		total += toolUseSystemPrompt
	}
	for _, tool := range req.Tools {
		total += toolDefinitionOverhead + textTokens(tool.Name) + textTokens(tool.Description) + textTokens(string(bytes.TrimSpace(tool.InputSchema)))
	}
	return roundTokens(total)
}

// EstimateChatInputTokens applies the same weights to a canonical chat
// request. It seeds the stream converter's message_start usage, where the
// Anthropic contract expects input tokens before the upstream has reported any.
func EstimateChatInputTokens(req *core.ChatRequest) int {
	if req == nil {
		return 0
	}
	total := 0.0
	for _, msg := range req.Messages {
		total += messageOverhead + partsTokens(msg.Content)
		for _, call := range msg.ToolCalls {
			total += toolBlockOverhead + textTokens(call.Function.Name) + textTokens(call.Function.Arguments)
		}
	}
	if len(req.Tools) > 0 {
		total += toolUseSystemPrompt
	}
	for _, tool := range req.Tools {
		if raw, err := json.Marshal(tool); err == nil {
			total += toolDefinitionOverhead + textTokens(string(raw))
		}
	}
	return roundTokens(total)
}

func contentBlockTokens(block ContentBlock) float64 {
	switch block.Type {
	case "image":
		return imageSourceTokens(block.Source)
	case "tool_use":
		return toolBlockOverhead + textTokens(block.Name) + textTokens(string(bytes.TrimSpace(block.Input)))
	case "tool_result":
		result, _ := toolResultContent(block.Content, true)
		return toolBlockOverhead + partsTokens(result)
	case "search_result":
		// Rendered for the model as its title, source, and body.
		text, _ := searchResultText(block)
		return textTokens(text)
	case "document":
		// Text documents are counted as text. A PDF costs by page and image
		// content, which the header does not reveal, so only its title is
		// counted and the rest is left to the provider's own count.
		if source, err := decodeSource(block.Source); err == nil && source != nil && source.Type == "text" {
			return textTokens(block.Title) + textTokens(source.Data)
		}
		return textTokens(block.Title)
	default:
		return textTokens(block.Text) + textTokens(block.Thinking)
	}
}

// partsTokens counts canonical message content: text, images given as data
// URLs, and files the way the wire estimate counts documents.
func partsTokens(content core.MessageContent) float64 {
	parts, ok := content.([]core.ContentPart)
	if !ok {
		return textTokens(core.ExtractTextContent(content))
	}
	total := 0.0
	for _, part := range parts {
		switch {
		case part.Type == "image_url" && part.ImageURL != nil:
			total += imageDataURLTokens(part.ImageURL.URL)
		case part.Type == "file" && part.File != nil:
			total += fileTokens(part.File)
		default:
			total += textTokens(part.Text)
		}
	}
	return total
}

// fileTokens mirrors the document rule of the wire estimate: a text file is
// counted as its text, anything else (a PDF) by its name only, since its page
// cost is not knowable here.
func fileTokens(file *core.FileContent) float64 {
	total := textTokens(file.Filename)
	if strings.HasPrefix(file.FileData, "data:text/") {
		if _, data, ok := strings.Cut(file.FileData, ";base64,"); ok {
			if decoded, err := base64.StdEncoding.DecodeString(data); err == nil {
				total += textTokens(string(decoded))
			}
		}
	}
	return total
}

// textTokens weights each character by how densely its class tokenizes.
// Long runs mixing letters and digits (hashes, ids, base64) are charged as a
// unit, because a tokenizer breaks them into fragments a few characters long.
func textTokens(s string) float64 {
	if s == "" {
		return 0
	}
	total := 0.0
	for field := range strings.FieldsFuncSeq(s, unicode.IsSpace) {
		if isBlob(field) {
			total += float64(len(field)) * tokensPerBlobChar
			continue
		}
		for _, r := range field {
			total += runeTokens(r)
		}
	}
	for _, r := range s {
		if unicode.IsSpace(r) {
			total += tokensPerLetter
		}
	}
	return total
}

func runeTokens(r rune) float64 {
	switch {
	case r < 0x80 && (unicode.IsLetter(r)):
		return tokensPerLetter
	case r < 0x80 && unicode.IsDigit(r):
		return tokensPerDigit
	case r < 0x80:
		return tokensPerSymbol
	case isEmoji(r):
		return tokensPerEmoji
	default:
		return tokensPerOtherRune
	}
}

func isEmoji(r rune) bool {
	return r >= 0x1F000 || (r >= 0x2600 && r <= 0x27BF) || r == 0x200D || (r >= 0xFE00 && r <= 0xFE0F)
}

func isBlob(field string) bool {
	if len(field) < blobMinLength {
		return false
	}
	letters, digits := false, false
	for _, r := range field {
		switch {
		case r >= 0x80:
			return false
		case unicode.IsLetter(r):
			letters = true
		case unicode.IsDigit(r):
			digits = true
		case r == '+' || r == '/' || r == '=' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return letters && digits
}

// imageSourceTokens prices an Anthropic image block. Dimensions are read from
// the encoded header of a base64 source; an image that cannot be measured
// (URL, file id, unsupported format) is charged the largest size Anthropic
// keeps, so the estimate errs toward not fitting rather than toward an
// upstream rejection.
func imageSourceTokens(raw json.RawMessage) float64 {
	source, err := decodeSource(raw)
	if err != nil || source == nil || source.Type != "base64" || source.Data == "" {
		return imageMaxTokens
	}
	return imageBase64Tokens(source.Data)
}

func imageDataURLTokens(url string) float64 {
	_, data, ok := strings.Cut(url, ";base64,")
	if !ok || !strings.HasPrefix(url, "data:") {
		return imageMaxTokens
	}
	return imageBase64Tokens(data)
}

func imageBase64Tokens(data string) float64 {
	config, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return imageMaxTokens
	}
	tokens := math.Ceil(float64(config.Width) * float64(config.Height) / imageTokenDivisor)
	return math.Min(tokens, imageMaxTokens)
}

// roundTokens rounds the estimate up and reports at least one token for any
// non-empty input.
func roundTokens(total float64) int {
	if total <= 0 {
		return 0
	}
	return max(int(math.Ceil(total)), 1)
}
