package core

import "strings"

// OperationPaths lists the request paths that DescribeEndpoint classifies as
// one operation, in a shape storage filters can match: Exact paths compare by
// equality, and each Prefix matches itself or anything under "Prefix/".
type OperationPaths struct {
	Exact    []string
	Prefixes []string
}

// operationPaths mirrors describeEndpointPath. TestOperationPathsMatchDescribeEndpoint
// keeps the two in sync.
var operationPaths = map[Operation]OperationPaths{
	OperationChatCompletions:     {Exact: []string{"/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens"}},
	OperationResponses:           {Prefixes: []string{"/v1/responses"}},
	OperationConversations:       {Prefixes: []string{"/v1/conversations"}},
	OperationEmbeddings:          {Exact: []string{"/v1/embeddings"}},
	OperationBatches:             {Prefixes: []string{"/v1/batches", "/v1/messages/batches"}},
	OperationFiles:               {Prefixes: []string{"/v1/files"}},
	OperationAudioSpeech:         {Exact: []string{"/v1/audio/speech"}},
	OperationAudioTranscriptions: {Exact: []string{"/v1/audio/transcriptions"}},
	OperationAudioTranslations:   {Exact: []string{"/v1/audio/translations"}},
	OperationImageGenerations:    {Exact: []string{"/v1/images/generations"}},
	OperationImageEdits:          {Exact: []string{"/v1/images/edits"}},
	OperationRealtime: {Exact: []string{
		"/v1/realtime", "/v1/realtime/calls", "/v1/realtime/client_secrets",
		"/v1/realtime/translations", "/v1/realtime/translations/calls", "/v1/realtime/translations/client_secrets",
	}},
	OperationMCP:                 {Prefixes: []string{"/mcp"}},
	OperationProviderPassthrough: {Prefixes: []string{"/p"}},
}

// PathsForOperation returns the paths of a known operation.
func PathsForOperation(op Operation) (OperationPaths, bool) {
	paths, ok := operationPaths[op]
	return paths, ok
}

// ParseOperations parses a comma-separated operation list, ignoring blanks
// and duplicates. It reports the first unknown name.
func ParseOperations(raw string) ([]Operation, string, bool) {
	var ops []Operation
	seen := map[Operation]bool{}
	for part := range strings.SplitSeq(raw, ",") {
		op := Operation(strings.ToLower(strings.TrimSpace(part)))
		if op == "" || seen[op] {
			continue
		}
		if _, ok := operationPaths[op]; !ok {
			return nil, string(op), false
		}
		seen[op] = true
		ops = append(ops, op)
	}
	return ops, "", true
}
