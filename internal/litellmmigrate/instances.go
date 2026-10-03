package litellmmigrate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// instanceFor returns the provider instance a deployment belongs to, creating
// it on first use. Deployments that share provider type, credentials, and
// endpoint share one GoModel provider.
func (c *converter) instanceFor(kind providerKind, p litellmParams, upstream, subject string) *instance {
	apiKey := p.APIKey
	if apiKey == "" && kind.KeyEnv != "" {
		apiKey = envRefPrefix + kind.KeyEnv
	}
	apiBase := p.APIBase
	if apiBase == "" && kind.BaseEnv != "" {
		apiBase = envRefPrefix + kind.BaseEnv
	}
	if kind.Type == "bedrock" && apiBase == "" {
		apiBase = p.AWSRegionName
	}
	// GoModel binds an Azure provider to one deployment.
	deployment := ""
	if kind.Type == "azure" {
		deployment = upstream
	}
	credentials := vertexCredentials(p.VertexCredentials)
	key := strings.Join([]string{kind.baseName(), apiKey, apiBase, p.APIVersion, p.VertexProject, p.VertexLocation, credentials, deployment}, "\x00")
	if inst, ok := c.byKey[key]; ok {
		return inst
	}

	keyEnv, isRef := envRef(apiKey)
	keyEnv = c.env.ref(keyEnv)
	literalKey := !isRef && apiKey != ""
	if literalKey && kind.KeyEnv != "" && !c.env.taken(kind.KeyEnv) {
		// An inline key moves to the .env file; LiteLLM's own default variable
		// is free, so it takes that name and the provider stays the default.
		keyEnv = kind.KeyEnv
	}
	inst := &instance{name: c.instanceName(kind, keyEnv, deployment), kind: kind}
	envPrefix := envName(inst.name)
	if keyEnv == "" {
		keyEnv = envPrefix + "_API_KEY"
	}
	out := &providerOut{Type: kind.Type}
	if apiKey != "" {
		out.APIKey = c.secretValue(apiKey, keyEnv)
	}
	if apiBase == "" {
		apiBase = kind.BaseURL
	}
	out.BaseURL = normalizeBaseURL(kind, c.plainValue(apiBase), deployment)
	out.APIVersion = c.plainValue(p.APIVersion)
	out.VertexProject = c.plainValue(p.VertexProject)
	out.VertexLocation = c.plainValue(p.VertexLocation)
	if credentials != "" {
		c.setVertexCredentials(out, credentials, envPrefix, subject)
	}
	inst.out = out
	c.byKey[key] = inst
	c.instances = append(c.instances, inst)
	return inst
}

// instanceName picks a unique provider name that agrees with GoModel's
// environment conventions. GoModel lets bare <TYPE>_API_KEY override the
// provider named after the type, and registers <TYPE>_<SUFFIX>_API_KEY as a
// provider named <type>-<suffix>. So the bare name goes to the provider that
// reads LiteLLM's default key variable, a provider reading
// OPENAI_EU_API_KEY becomes openai-eu, and anything else is numbered. Azure
// providers are named after their deployment, one provider per deployment.
func (c *converter) instanceName(kind providerKind, keyEnv, deployment string) string {
	base := kind.baseName()
	var candidates []string
	switch {
	case deployment != "":
		if s := slug(deployment); s != "" {
			candidates = append(candidates, base+"-"+s)
		}
	case kind.KeyEnv == "" || keyEnv == kind.KeyEnv:
		candidates = append(candidates, base)
	}
	if suffix, ok := envSuffix(keyEnv, envName(base)); ok && deployment == "" {
		candidates = append(candidates, base+"-"+suffix)
	}
	for _, name := range candidates {
		if !c.usedNames[name] {
			c.usedNames[name] = true
			return name
		}
	}
	for i := 1; ; i++ {
		name := fmt.Sprintf("%s-%d", base, i)
		if !c.usedNames[name] {
			c.usedNames[name] = true
			return name
		}
	}
}

// envSuffix extracts SUFFIX from <PREFIX>_<SUFFIX>_API_KEY as a lower-case,
// hyphenated provider name suffix.
func envSuffix(keyEnv, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(keyEnv, prefix+"_")
	if !ok {
		return "", false
	}
	suffix, ok := strings.CutSuffix(rest, "_API_KEY")
	if !ok || suffix == "" {
		return "", false
	}
	return strings.ToLower(strings.ReplaceAll(suffix, "_", "-")), true
}

// vertexCredentials flattens vertex_credentials, which LiteLLM accepts as a
// file path, an inline JSON string, an os.environ/ reference, or a YAML map.
func vertexCredentials(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

func (c *converter) setVertexCredentials(out *providerOut, credentials, envPrefix, subject string) {
	out.AuthType = "gcp_service_account"
	if name, ok := c.envRef(credentials); ok {
		out.ServiceAccountJSON = "${" + name + "}"
		c.env.require(name)
		c.report.warn(subject, fmt.Sprintf("vertex_credentials reads %s; it was mapped to service_account_json, so if %s holds a file path move it to service_account_file", name, name))
		return
	}
	if strings.HasPrefix(credentials, "{") {
		out.ServiceAccountJSON = c.secretValue(credentials, envPrefix+"_SERVICE_ACCOUNT_JSON")
		return
	}
	out.ServiceAccountFile = credentials
}

// secretValue turns a LiteLLM secret into a config value. An os.environ/NAME
// reference becomes ${NAME}; a literal moves to the generated .env file, so
// the config file itself never carries a secret.
func (c *converter) secretValue(value, envName string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if name, ok := c.envRef(value); ok {
		c.env.require(name)
		return "${" + name + "}"
	}
	return "${" + c.env.set(envName, value) + "}"
}

// plainValue converts a non-secret LiteLLM value, keeping literals inline.
func (c *converter) plainValue(value string) string {
	value = strings.TrimSpace(value)
	if name, ok := c.envRef(value); ok {
		c.env.require(name)
		return "${" + name + "}"
	}
	return value
}

// envRef resolves an os.environ/ reference to the variable it reads in the
// generated files.
func (c *converter) envRef(value string) (string, bool) {
	name, ok := envRef(value)
	return c.env.ref(name), ok
}

// envName derives an environment variable prefix from a provider name, the
// same way GoModel maps <PROVIDER>_<SUFFIX>_* variables to provider names.
func envName(providerName string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(providerName))
}
