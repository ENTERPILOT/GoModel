package litellmmigrate

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// converter holds the state of one conversion.
type converter struct {
	src    *liteLLMConfig
	out    gomodelConfig
	report Report
	env    envFile

	router  *section
	litellm *section
	general *section

	credentials map[string]map[string]string
	instances   []*instance
	byKey       map[string]*instance
	usedNames   map[string]bool
	groups      []*group
	groupByName map[string]*group
	// wildcard is set when a deployment exposes a provider's whole catalog,
	// which GoModel serves without virtual models.
	wildcard bool
	// poolNames holds the load balancer names generated for groups with
	// fallbacks.
	poolNames []string
	// usageBasedRouting is set under LiteLLM's usage-based routing, the one
	// strategy where LiteLLM enforces deployment rpm/tpm.
	usageBasedRouting bool
}

// instance is one GoModel provider: a distinct combination of provider type,
// credentials, and endpoint across the LiteLLM deployments.
type instance struct {
	name      string
	kind      providerKind
	out       *providerOut
	models    []string
	metadata  map[string]*core.ModelMetadata
	patterns  []string
	wildcard  bool
	modelUses []string
}

func newConverter(src *liteLLMConfig, source string) *converter {
	c := &converter{
		src:         src,
		router:      newSection("router_settings", src.RouterSettings),
		litellm:     newSection("litellm_settings", src.LiteLLMSettings),
		general:     newSection("general_settings", src.GeneralSettings),
		credentials: map[string]map[string]string{},
		byKey:       map[string]*instance{},
		usedNames:   map[string]bool{},
		groupByName: map[string]*group{},
	}
	c.report.Source = source
	for _, cred := range src.CredentialList {
		c.credentials[cred.Name] = cred.Values
	}
	return c
}

func (c *converter) run() {
	c.report.Deployments = len(c.src.ModelList)
	// Values the LiteLLM config sets or reads by name keep those names;
	// claim them before any inline secret is given a variable.
	c.convertEnvironmentVariables()
	c.reserveEnvRefs()
	for i, d := range c.src.ModelList {
		c.addDeployment(i, d)
	}
	c.finalizeInstances()
	c.buildVirtualModels()
	c.convertSettings()
}

func (c *converter) addDeployment(index int, d deployment) {
	subject := fmt.Sprintf("model_list[%d]", index)
	if d.ModelName != "" {
		subject += " (" + d.ModelName + ")"
	}
	p := d.LiteLLMParams
	if strings.TrimSpace(d.ModelName) == "" || strings.TrimSpace(p.Model) == "" {
		c.report.skip(subject, "model_name and litellm_params.model are both required")
		return
	}
	c.applyCredential(&p, subject)

	prefix, upstream := splitModel(p.Model, p.CustomLLMProvider)
	if prefix == "*" {
		c.wildcard = true
		c.report.info(subject, "a catch-all `*` deployment needs no config: GoModel serves every model of every configured provider, including providers registered from environment keys")
		return
	}
	kind, ok := providerKinds[prefix]
	if !ok {
		if prefix == "" {
			c.report.skip(subject, fmt.Sprintf("cannot tell which provider serves %q; add a provider prefix such as openai/ and run again", p.Model))
		} else {
			c.report.skip(subject, fmt.Sprintf("LiteLLM provider %q has no GoModel equivalent yet", prefix))
		}
		return
	}
	upstream = normalizeUpstream(prefix, upstream)
	inst := c.instanceFor(kind, p, upstream, subject)
	inst.modelUses = appendUnique(inst.modelUses, d.ModelName)
	c.reportUnmigratedParams(subject, p, d)

	if strings.Contains(upstream, "*") {
		c.addWildcard(subject, d.ModelName, prefix, upstream, inst)
		return
	}
	inst.models = appendUnique(inst.models, upstream)
	c.addMetadata(subject, inst, upstream, p, d.ModelInfo)
	c.groupFor(d.ModelName).add(targetOut{Provider: inst.name, Model: upstream}, p)
}

// reserveEnvRefs reserves every variable the LiteLLM config reads: each
// os.environ/ reference anywhere in it, and the default key variable of each
// deployment that sets no api_key.
func (c *converter) reserveEnvRefs() {
	for _, name := range collectEnvRefs(c.src) {
		c.env.reserve(name)
	}
	for _, d := range c.src.ModelList {
		p := d.LiteLLMParams
		if values, ok := c.credentials[p.CredentialName]; ok && p.APIKey == "" {
			p.APIKey = values["api_key"]
		}
		prefix, _ := splitModel(p.Model, p.CustomLLMProvider)
		if kind, ok := providerKinds[prefix]; ok && p.APIKey == "" && kind.KeyEnv != "" {
			c.env.reserve(kind.KeyEnv)
		}
	}
}

// applyCredential fills unset connection fields from a credential_list entry.
func (c *converter) applyCredential(p *litellmParams, subject string) {
	if p.CredentialName == "" {
		return
	}
	values, ok := c.credentials[p.CredentialName]
	if !ok {
		c.report.warn(subject, fmt.Sprintf("litellm_credential_name %q is not in credential_list", p.CredentialName))
		return
	}
	fill := func(field *string, key string) {
		if *field == "" {
			*field = values[key]
		}
	}
	fill(&p.APIKey, "api_key")
	fill(&p.APIBase, "api_base")
	fill(&p.APIVersion, "api_version")
	fill(&p.VertexProject, "vertex_project")
	fill(&p.VertexLocation, "vertex_location")
	if p.VertexCredentials == nil && values["vertex_credentials"] != "" {
		p.VertexCredentials = values["vertex_credentials"]
	}
}

func (c *converter) addWildcard(subject, modelName, prefix, upstream string, inst *instance) {
	c.wildcard = true
	if upstream == "*" {
		inst.wildcard = true
	} else {
		inst.patterns = appendUnique(inst.patterns, upstream)
	}
	if modelName == "*" || (modelName == prefix+"/"+upstream && inst.name == prefix) {
		return
	}
	c.report.warn(subject, fmt.Sprintf("clients that called %q now address these models as %s/<model>", modelName, inst.name))
}

func (c *converter) addMetadata(subject string, inst *instance, upstream string, p litellmParams, info modelInfo) {
	meta := modelMetadata(p, info)
	if meta == nil {
		return
	}
	if inst.metadata == nil {
		inst.metadata = map[string]*core.ModelMetadata{}
	}
	if _, exists := inst.metadata[upstream]; exists {
		c.report.warn(subject, fmt.Sprintf("%s/%s already has pricing or limits from an earlier deployment; kept the first", inst.name, upstream))
		return
	}
	inst.metadata[upstream] = meta
}

// modelMetadata converts LiteLLM's per-token prices and token limits.
// model_info values win over litellm_params ones.
func modelMetadata(p litellmParams, info modelInfo) *core.ModelMetadata {
	pick := func(primary, fallback *float64) *float64 {
		if primary != nil {
			return perMtok(*primary)
		}
		if fallback != nil {
			return perMtok(*fallback)
		}
		return nil
	}
	pricing := &core.ModelPricing{
		Currency:           "USD",
		InputPerMtok:       pick(info.InputCostPerToken, p.InputCostPerToken),
		OutputPerMtok:      pick(info.OutputCostPerToken, p.OutputCostPerToken),
		CachedInputPerMtok: pick(info.CacheReadInputTokenCost, p.CacheReadInputTokenCost),
		CacheWritePerMtok:  pick(info.CacheCreationInputTokenCost, p.CacheCreationInputTokenCost),
	}
	meta := &core.ModelMetadata{
		ContextWindow:   info.MaxInputTokens,
		MaxOutputTokens: info.MaxOutputTokens,
	}
	if pricing.InputPerMtok != nil || pricing.OutputPerMtok != nil || pricing.CachedInputPerMtok != nil || pricing.CacheWritePerMtok != nil {
		meta.Pricing = pricing
	}
	if meta.Pricing == nil && meta.ContextWindow == nil && meta.MaxOutputTokens == nil {
		return nil
	}
	return meta
}

// perMtok converts a per-token price to a per-million-token price, rounded to
// drop floating-point noise (2.5e-06 * 1e6 is 2.4999999999999996).
func perMtok(perToken float64) *float64 {
	v := math.Round(perToken*1e6*1e9) / 1e9
	return &v
}

func (c *converter) reportUnmigratedParams(subject string, p litellmParams, d deployment) {
	if len(d.ModelInfo.AccessGroups) > 0 {
		c.report.warn(subject, fmt.Sprintf("model_info.access_groups %s not migrated: LiteLLM limits this model to keys and teams in those groups, while any GoModel key can call it until you set allowed models on the user paths or keys", strings.Join(d.ModelInfo.AccessGroups, ", ")))
	}
	if p.AWSAccessKeyID != "" || p.AWSSecretAccessKey != "" || p.AWSProfileName != "" {
		c.report.warn(subject, "GoModel's Bedrock provider authenticates with the standard AWS credential chain; set AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY or AWS_PROFILE for the gateway instead of per-deployment credentials")
	}
	var keys []string
	for key := range p.Extra {
		keys = append(keys, "litellm_params."+key)
	}
	for key := range d.Extra {
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	c.report.skip(subject, "not migrated: "+strings.Join(keys, ", "))
}

func (c *converter) finalizeInstances() {
	c.out.Providers = map[string]*providerOut{}
	allowlist := false
	shadowNoted := map[string]bool{}
	for _, inst := range c.instances {
		c.out.Providers[inst.name] = inst.out
		switch {
		case inst.wildcard:
			c.reportDroppedMetadata(inst, "it serves its whole model catalog")
		case len(inst.patterns) > 0:
			inst.out.ModelFilter = &modelFilterOut{Include: append(slices.Clone(inst.patterns), inst.models...)}
			c.reportDroppedMetadata(inst, "it uses a wildcard model filter")
		default:
			allowlist = true
			for _, id := range inst.models {
				inst.out.Models = append(inst.out.Models, providerModel{ID: id, Metadata: inst.metadata[id]})
			}
		}
		c.report.Providers = append(c.report.Providers, providerRow{
			Name: inst.name, Type: inst.kind.Type, BaseURL: inst.out.BaseURL, ModelNames: inst.modelUses,
		})
		if inst.kind.native() && inst.kind.KeyEnv != "" && !c.usedNames[inst.kind.Type] && !shadowNoted[inst.kind.Type] {
			shadowNoted[inst.kind.Type] = true
			c.report.info("providers."+inst.name, fmt.Sprintf("if %s_API_KEY is set in GoModel's environment, GoModel also registers a provider named %q from it", envName(inst.kind.Type), inst.kind.Type))
		}
	}
	if allowlist {
		c.models().ConfiguredProviderModelsMode = "allowlist"
	}
}

func (c *converter) reportDroppedMetadata(inst *instance, reason string) {
	for _, id := range inst.models {
		if inst.metadata[id] != nil {
			c.report.warn("providers."+inst.name, fmt.Sprintf("pricing and token limits for %s were not migrated because %s; set them as pricing overrides in the dashboard", id, reason))
		}
	}
}

func (c *converter) models() *modelsOut {
	if c.out.Models == nil {
		c.out.Models = &modelsOut{}
	}
	return c.out.Models
}

func appendUnique(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}
