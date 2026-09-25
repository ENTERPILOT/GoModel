package audiocpp

import (
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// The keys are endpoints as a passthrough request spells them, without the
// optional /v1 alias the gateway strips before a provider sees them. The
// routes with no GenAI operation are the ones that are not model inference in
// the gateway's vocabulary, or not inference at all.
var passthroughSemanticEnricher = providers.NewSemanticEnricher("audiocpp", map[string]providers.PassthroughEndpointSemantics{
	"/audio/speech": {
		Operation:      "audiocpp.audio_speech",
		GenAIOperation: string(core.OperationAudioSpeech),
		AuditPath:      "/v1/audio/speech",
	},
	"/audio/speech/live": {
		Operation:      "audiocpp.audio_speech_live",
		GenAIOperation: string(core.OperationAudioSpeech),
		AuditPath:      "/v1/audio/speech/live",
	},
	"/audio/transcriptions": {
		Operation:      "audiocpp.audio_transcriptions",
		GenAIOperation: string(core.OperationAudioTranscriptions),
		AuditPath:      "/v1/audio/transcriptions",
	},
	"/audio/transcriptions/details": {
		Operation:      "audiocpp.audio_transcriptions_details",
		GenAIOperation: string(core.OperationAudioTranscriptions),
		AuditPath:      "/v1/audio/transcriptions/details",
	},
	"/audio/transcriptions/live": {
		Operation:      "audiocpp.audio_transcriptions_live",
		GenAIOperation: string(core.OperationAudioTranscriptions),
		AuditPath:      "/v1/audio/transcriptions/live",
	},
	"/audio/alignments": {
		Operation: "audiocpp.audio_alignments",
		AuditPath: "/v1/audio/alignments",
	},
	"/tasks/run": {
		Operation: "audiocpp.tasks_run",
		AuditPath: "/v1/tasks/run",
	},
	"/tasks/stream": {
		Operation: "audiocpp.tasks_stream",
		AuditPath: "/v1/tasks/stream",
	},
})
