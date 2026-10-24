package pluginapi

import "context"

// DecisionsHost is implemented by hosts that let plugins ask decision models
// typed questions. It is optional: assert it on the [Host] passed to Init.
//
//	if dh, ok := host.(pluginapi.DecisionsHost); ok { ... }
type DecisionsHost interface {
	// Decisions asks decision models typed questions through the gateway.
	// Routing, failover, usage accounting, and budgets apply as for
	// [Host.Inference]; requests carry origin "plugin".
	Decisions() Decisions
}

// Decisions asks a decision model typed questions about an input, the way
// POST /v1/decisions does. Decision models (OpenAI's Decisions API, Jev,
// Kev, OpenRouter's decision models) answer with calibrated probabilities
// instead of generated text, which suits classification such as policy
// checks.
type Decisions interface {
	Decide(ctx context.Context, req DecisionRequest) (*DecisionResult, error)
	// DecisionModel reports whether model answers decisions only and cannot
	// chat, as Jev, Kev, and OpenRouter's decision models do. A virtual
	// model is one when every target is. A model the gateway cannot describe
	// reports false. A model that does both, such as an OpenAI model with
	// Decisions support, reports false too.
	DecisionModel(model string) bool
}

// QuestionType is the kind of a [Question].
type QuestionType string

const (
	// QuestionPredicate asks a yes-or-no question; the answer is the
	// probability of yes.
	QuestionPredicate QuestionType = "predicate"
	// QuestionChoice asks for one of [Question.Choices].
	QuestionChoice QuestionType = "choice"
	// QuestionScore asks for a level of [Question.Levels].
	QuestionScore QuestionType = "score"
)

// DecisionRequest describes questions to ask a decision model.
type DecisionRequest struct {
	// Model is a "provider/model" reference, an alias, or a virtual model.
	Model string
	// UserPath optionally overrides the user path the internal call is scoped
	// to (for budgets and audit). Empty means the current request's path.
	UserPath string
	// Input is what the questions are about. Every message is sent as a user
	// message; its text and image parts are sent, other parts are dropped.
	// Whether a model reads images depends on the model.
	Input []Message
	// Questions are answered in order.
	Questions []Question
}

// Question is one typed question of a [DecisionRequest].
type Question struct {
	Type QuestionType
	// Name identifies the answer; optional.
	Name string
	// Instructions is the question itself.
	Instructions string
	// Choices are the options of a [QuestionChoice].
	Choices []ChoiceOption
	// Levels are the ordered levels of a [QuestionScore], lowest first.
	Levels []ScoreLevel
}

// ChoiceOption is one option of a [QuestionChoice] question.
type ChoiceOption struct {
	Value       string
	Description string
}

// ScoreLevel is one level of a [QuestionScore] question.
type ScoreLevel struct {
	Label       string
	Description string
}

// AnswerRefusal is the [Answer.Type] of a question the model did not answer.
const AnswerRefusal = "refusal"

// DecisionResult is a decision model's answer, one [Answer] per question in
// order.
type DecisionResult struct {
	Model   string
	Answers []Answer
}

// Answer answers one [Question].
type Answer struct {
	Name string
	// Type is the question's type, or [AnswerRefusal] when the model did not
	// answer it.
	Type string
	// Probability is a predicate's probability of yes.
	Probability float64
	// Choice is the most likely choice of a choice question.
	Choice string
	// Score is the expected level of a score question, by level index.
	Score float64
	// Confidence is the probability of Choice, or of the most likely level.
	Confidence float64
	// Probabilities is the distribution of a choice or score answer.
	Probabilities []AnswerProbability
}

// Refused reports whether the model did not answer the question.
func (a Answer) Refused() bool { return a.Type == AnswerRefusal }

// AnswerProbability is the probability of one choice value or level label.
type AnswerProbability struct {
	Value       string
	Probability float64
}
