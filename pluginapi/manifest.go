package pluginapi

// Kind names a hook a plugin implements. A [Manifest] lists its Kinds so
// GoModel can validate configuration before calling anything; at load time the
// list is checked against the interfaces the plugin value actually satisfies.
type Kind string

const (
	// KindRequest marks a [RequestHook]: runs before model resolution.
	KindRequest Kind = "request"
	// KindPrompt marks a [PromptHook]: runs after routing, before the provider call.
	KindPrompt Kind = "prompt"
	// KindResponse marks a [ResponseHook]: runs on a complete response.
	KindResponse Kind = "response"
	// KindStream marks a [StreamHook]: runs per streamed event.
	KindStream Kind = "stream"
	// KindRoute marks a [RouteStrategy]: picks a target for a virtual model.
	KindRoute Kind = "route"
	// KindComplete marks a [CompleteHook]: runs after the client response is written.
	KindComplete Kind = "complete"
)

// BuildInfo records the toolchain a plugin binary was built with. It is
// filled by the `gomodel plugin build` helper and used only to produce a
// readable error when a shared object cannot be loaded.
type BuildInfo struct {
	// GoVersion is the Go toolchain version, for example "go1.27.0".
	GoVersion string
	// PluginAPIVersion is the [Version] of this package at build time.
	PluginAPIVersion string
}

// Manifest describes a plugin type: its identity, the hooks it implements, and
// the configuration form it needs.
type Manifest struct {
	// Name is the stable identifier used in configuration and workflows.
	Name string
	// Version is the plugin's own version, shown in logs and the dashboard.
	Version string
	// Description is a one-line summary for the dashboard.
	Description string
	// BuiltWith is filled by the build helper; leave empty otherwise.
	BuiltWith BuildInfo
	// Kinds lists the hooks the plugin implements.
	Kinds []Kind
	// Mutates declares that the plugin edits the Prompt, Completion, or
	// stream. Non-mutating plugins may run concurrently with the provider call.
	Mutates bool
	// ConfigSchema drives the dashboard form and config validation. The
	// validated config is passed to [Plugin.Init] as JSON.
	ConfigSchema []Field
}

// Input selects the dashboard control used to edit a [Field].
type Input string

const (
	// InputText is a single-line text box.
	InputText Input = "text"
	// InputTextarea is a multi-line text box.
	InputTextarea Input = "textarea"
	// InputNumber is a numeric input.
	InputNumber Input = "number"
	// InputSelect is a single-choice dropdown over Field.Options.
	InputSelect Input = "select"
	// InputCheckboxes is a multi-choice list over Field.Options.
	InputCheckboxes Input = "checkboxes"
	// InputSecret is a masked text box; the value is stored encrypted.
	InputSecret Input = "secret"
	// InputModel is a model picker listing the gateway's models.
	InputModel Input = "model"
)

// FieldScope says which editor shows a [Field].
type FieldScope string

const (
	// ScopeInstance (the default) shows the field in the plugin instance editor.
	ScopeInstance FieldScope = ""
	// ScopeRoute shows the field in the virtual model editor; used by
	// [RouteStrategy] plugins whose settings belong to a route.
	ScopeRoute FieldScope = "route"
)

// Option is one choice of a select or checkboxes [Field].
type Option struct {
	// Value is stored in the config.
	Value string
	// Label is shown to the operator.
	Label string
}

// Field is one dashboard form field and one validated configuration key.
type Field struct {
	// Key is the JSON key in the instance config.
	Key string
	// Label is the human-readable name shown next to the control.
	Label string
	// Input selects the control; defaults to [InputText].
	Input Input
	// Required rejects configs that omit the key.
	Required bool
	// Help is a short explanation shown under the control.
	Help string
	// Placeholder is the control's placeholder text.
	Placeholder string
	// Default is used when the key is absent. Must be JSON-serializable.
	Default any
	// Options lists the choices of select and checkboxes inputs.
	Options []Option
	// Scope selects the editor; see [FieldScope].
	Scope FieldScope
}
