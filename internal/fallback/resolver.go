package fallback

import (
	"math"
	"sort"
	"strings"

	"gomodel/config"
	"gomodel/internal/core"
	"gomodel/internal/providers"
)

const maxAutoFallbackCandidates = 5

var preferredRankingNames = []string{
	"chatbot_arena",
	"chatbot_arena_coding",
	"chatbot_arena_math",
	"chatbot_arena_creative_writing",
	"chatbot_arena_vision",
}

// Registry is the minimal provider inventory surface needed for fallback
// candidate resolution.
type Registry interface {
	GetModel(model string) *providers.ModelInfo
	ListModelsWithProvider() []providers.ModelWithProvider
}

// Resolver computes fallback model chains for translated routes.
type Resolver struct {
	defaultMode config.FallbackMode
	manual      map[string][]string
	overrides   map[string]config.FallbackModelOverride
	registry    Registry
}

// NewResolver builds a fallback resolver from config and the current model
// inventory. Returns nil when fallback is effectively disabled.
func NewResolver(cfg config.FallbackConfig, registry Registry) *Resolver {
	if registry == nil {
		return nil
	}

	mode := cfg.DefaultMode
	if mode == "" {
		mode = config.FallbackModeOff
	}
	if mode == config.FallbackModeOff && len(cfg.Manual) == 0 && len(cfg.Overrides) == 0 {
		return nil
	}

	manual := make(map[string][]string, len(cfg.Manual))
	for key, models := range cfg.Manual {
		copyModels := append([]string(nil), models...)
		manual[key] = copyModels
	}

	overrides := make(map[string]config.FallbackModelOverride, len(cfg.Overrides))
	for key, override := range cfg.Overrides {
		overrides[key] = override
	}

	return &Resolver{
		defaultMode: mode,
		manual:      manual,
		overrides:   overrides,
		registry:    registry,
	}
}

// ResolveFallbacks returns the ordered fallback chain for a resolved request.
// Manual fallbacks preserve configured order; auto candidates are appended after
// manual candidates when the effective mode is "auto".
func (r *Resolver) ResolveFallbacks(resolution *core.RequestModelResolution, op core.Operation) []core.ModelSelector {
	if r == nil || resolution == nil || r.registry == nil {
		return nil
	}

	source := r.sourceModelInfo(resolution)
	mode := r.modeFor(resolution, source)
	if mode == config.FallbackModeOff {
		return nil
	}

	sourceKey := r.sourceKey(resolution, source)
	seen := make(map[string]struct{})

	result := r.manualSelectorsFor(resolution, source, sourceKey, seen)
	if mode != config.FallbackModeAuto {
		return result
	}

	requiredCategory := requiredCategoryForOperation(op)
	return append(result, r.autoSelectorsFor(source, sourceKey, requiredCategory, seen)...)
}

func (r *Resolver) sourceModelInfo(resolution *core.RequestModelResolution) *providers.ModelInfo {
	if resolution == nil || r.registry == nil {
		return nil
	}

	keys := []string{
		resolution.ResolvedQualifiedModel(),
		resolution.ResolvedSelector.Model,
		resolution.RequestedQualifiedModel(),
		resolution.Requested.Model,
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if info := r.registry.GetModel(key); info != nil {
			return info
		}
	}
	return nil
}

func (r *Resolver) modeFor(resolution *core.RequestModelResolution, source *providers.ModelInfo) config.FallbackMode {
	mode := r.defaultMode
	for _, key := range r.matchKeys(resolution, source) {
		override, ok := r.overrides[key]
		if !ok || override.Mode == "" {
			continue
		}
		return override.Mode
	}
	return mode
}

func (r *Resolver) manualSelectorsFor(
	resolution *core.RequestModelResolution,
	source *providers.ModelInfo,
	sourceKey string,
	seen map[string]struct{},
) []core.ModelSelector {
	for _, key := range r.matchKeys(resolution, source) {
		models, ok := r.manual[key]
		if !ok {
			continue
		}
		result := make([]core.ModelSelector, 0, len(models))
		for _, model := range models {
			selector, candidateKey, ok := r.resolveSelector(model)
			if !ok || candidateKey == sourceKey {
				continue
			}
			if _, exists := seen[candidateKey]; exists {
				continue
			}
			seen[candidateKey] = struct{}{}
			result = append(result, selector)
		}
		return result
	}
	return nil
}

func (r *Resolver) autoSelectorsFor(
	source *providers.ModelInfo,
	sourceKey string,
	requiredCategory core.ModelCategory,
	seen map[string]struct{},
) []core.ModelSelector {
	if source == nil || source.Model.Metadata == nil {
		return nil
	}

	sourceRankingName, sourceRanking, ok := preferredRanking(source.Model.Metadata.Rankings)
	if !ok {
		return nil
	}

	type scoredCandidate struct {
		selector          core.ModelSelector
		key               string
		sameModelID       bool
		sameFamily        bool
		scoreDiff         float64
		hasScoreDiff      bool
		rankDiff          int
		hasRankDiff       bool
		capabilityOverlap int
	}

	sourceMeta := source.Model.Metadata
	candidates := make([]scoredCandidate, 0)
	for _, candidate := range r.registry.ListModelsWithProvider() {
		key := strings.TrimSpace(candidate.Selector)
		if key == "" || key == sourceKey {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}

		meta := candidate.Model.Metadata
		if meta == nil {
			continue
		}
		if !supportsCategory(meta, requiredCategory) {
			continue
		}

		candidateRanking, ok := rankingByName(meta.Rankings, sourceRankingName)
		if !ok {
			continue
		}

		entry := scoredCandidate{
			selector: core.ModelSelector{
				Model:    candidate.Model.ID,
				Provider: candidate.ProviderName,
			},
			key:               key,
			sameModelID:       candidate.Model.ID == source.Model.ID,
			sameFamily:        sameFamily(sourceMeta, meta),
			capabilityOverlap: capabilityOverlap(sourceMeta.Capabilities, meta.Capabilities),
		}
		if sourceRanking.Elo != nil && candidateRanking.Elo != nil {
			entry.hasScoreDiff = true
			entry.scoreDiff = math.Abs(*candidateRanking.Elo - *sourceRanking.Elo)
		}
		if sourceRanking.Rank != nil && candidateRanking.Rank != nil {
			entry.hasRankDiff = true
			entry.rankDiff = absInt(*candidateRanking.Rank - *sourceRanking.Rank)
		}
		candidates = append(candidates, entry)
	}

	sort.Slice(candidates, func(i, j int) bool {
		a := candidates[i]
		b := candidates[j]
		if a.sameModelID != b.sameModelID {
			return a.sameModelID
		}
		if a.sameFamily != b.sameFamily {
			return a.sameFamily
		}
		if a.hasScoreDiff != b.hasScoreDiff {
			return a.hasScoreDiff
		}
		if a.hasScoreDiff && a.scoreDiff != b.scoreDiff {
			return a.scoreDiff < b.scoreDiff
		}
		if a.hasRankDiff != b.hasRankDiff {
			return a.hasRankDiff
		}
		if a.hasRankDiff && a.rankDiff != b.rankDiff {
			return a.rankDiff < b.rankDiff
		}
		if a.capabilityOverlap != b.capabilityOverlap {
			return a.capabilityOverlap > b.capabilityOverlap
		}
		return a.key < b.key
	})

	limit := maxAutoFallbackCandidates
	if len(candidates) < limit {
		limit = len(candidates)
	}

	result := make([]core.ModelSelector, 0, limit)
	for i := 0; i < limit; i++ {
		seen[candidates[i].key] = struct{}{}
		result = append(result, candidates[i].selector)
	}
	return result
}

func (r *Resolver) resolveSelector(model string) (core.ModelSelector, string, bool) {
	model = strings.TrimSpace(model)
	if model == "" || r.registry == nil {
		return core.ModelSelector{}, "", false
	}

	info := r.registry.GetModel(model)
	if info == nil {
		return core.ModelSelector{}, "", false
	}

	selector := core.ModelSelector{
		Model:    info.Model.ID,
		Provider: info.ProviderName,
	}
	return selector, selector.QualifiedModel(), true
}

func (r *Resolver) sourceKey(resolution *core.RequestModelResolution, source *providers.ModelInfo) string {
	if source != nil && source.ProviderName != "" && source.Model.ID != "" {
		return source.ProviderName + "/" + source.Model.ID
	}
	return strings.TrimSpace(resolution.ResolvedQualifiedModel())
}

func (r *Resolver) matchKeys(resolution *core.RequestModelResolution, source *providers.ModelInfo) []string {
	keys := []string{
		resolution.RequestedQualifiedModel(),
		resolution.Requested.Model,
	}
	if source != nil && source.ProviderName != "" && source.Model.ID != "" {
		keys = append(keys, source.ProviderName+"/"+source.Model.ID)
	}
	keys = append(keys,
		resolution.ResolvedQualifiedModel(),
		resolution.ResolvedSelector.Model,
	)

	seen := make(map[string]struct{}, len(keys))
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func requiredCategoryForOperation(op core.Operation) core.ModelCategory {
	switch op {
	case core.OperationChatCompletions, core.OperationResponses:
		return core.CategoryTextGeneration
	case core.OperationEmbeddings:
		return core.CategoryEmbedding
	default:
		return ""
	}
}

func supportsCategory(meta *core.ModelMetadata, required core.ModelCategory) bool {
	if meta == nil {
		return false
	}
	if required == "" {
		return true
	}
	for _, category := range meta.Categories {
		if category == required {
			return true
		}
	}
	return false
}

func preferredRanking(rankings map[string]core.ModelRanking) (string, core.ModelRanking, bool) {
	if len(rankings) == 0 {
		return "", core.ModelRanking{}, false
	}
	for _, name := range preferredRankingNames {
		if ranking, ok := rankingByName(rankings, name); ok {
			return name, ranking, true
		}
	}

	keys := make([]string, 0, len(rankings))
	for name := range rankings {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if ranking, ok := rankingByName(rankings, name); ok {
			return name, ranking, true
		}
	}
	return "", core.ModelRanking{}, false
}

func rankingByName(rankings map[string]core.ModelRanking, name string) (core.ModelRanking, bool) {
	ranking, ok := rankings[name]
	if !ok {
		return core.ModelRanking{}, false
	}
	if ranking.Elo == nil && ranking.Rank == nil {
		return core.ModelRanking{}, false
	}
	return ranking, true
}

func sameFamily(source, candidate *core.ModelMetadata) bool {
	if source == nil || candidate == nil {
		return false
	}
	if strings.TrimSpace(source.Family) == "" || strings.TrimSpace(candidate.Family) == "" {
		return false
	}
	return source.Family == candidate.Family
}

func capabilityOverlap(source, candidate map[string]bool) int {
	if len(source) == 0 || len(candidate) == 0 {
		return 0
	}
	overlap := 0
	for key, enabled := range source {
		if !enabled || !candidate[key] {
			continue
		}
		overlap++
	}
	return overlap
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
