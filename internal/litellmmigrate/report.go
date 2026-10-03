package litellmmigrate

import (
	"fmt"
	"strings"
)

// Severity classifies a report finding.
type Severity string

const (
	// SeverityInfo marks a setting that was converted with a behavior change
	// worth knowing about.
	SeverityInfo Severity = "info"
	// SeverityWarning marks something the operator must review or finish.
	SeverityWarning Severity = "warning"
	// SeveritySkipped marks a setting that was not migrated.
	SeveritySkipped Severity = "skipped"
)

// Finding is one report entry about a LiteLLM setting.
type Finding struct {
	Severity Severity
	Subject  string
	Message  string
}

// Report describes what a conversion produced and what it left behind.
type Report struct {
	Source        string
	Deployments   int
	Providers     []providerRow
	VirtualModels []virtualModelOut
	// RequiredEnv lists variables the LiteLLM config read from the
	// environment; GoModel needs them too.
	RequiredEnv []string
	// WrittenEnv lists variables written to the generated .env file.
	WrittenEnv []string
	Findings   []Finding
}

type providerRow struct {
	Name       string
	Type       string
	BaseURL    string
	ModelNames []string
}

func (r *Report) add(severity Severity, subject, message string) {
	r.Findings = append(r.Findings, Finding{Severity: severity, Subject: subject, Message: message})
}

func (r *Report) info(subject, message string) { r.add(SeverityInfo, subject, message) }
func (r *Report) warn(subject, message string) { r.add(SeverityWarning, subject, message) }
func (r *Report) skip(subject, message string) { r.add(SeveritySkipped, subject, message) }

// Count returns the number of findings with the given severity.
func (r *Report) Count(severity Severity) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == severity {
			n++
		}
	}
	return n
}

// Markdown renders the report for MIGRATION_REPORT.md and the dry-run output.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# LiteLLM to GoModel migration report\n\n")
	fmt.Fprintf(&b, "Source: `%s`\n\n", r.Source)
	fmt.Fprintf(&b, "%d deployments became %d providers and %d virtual models. %d warnings, %d settings not migrated.\n",
		r.Deployments, len(r.Providers), len(r.VirtualModels), r.Count(SeverityWarning), r.Count(SeveritySkipped))

	if len(r.Providers) > 0 {
		b.WriteString("\n## Providers\n\n| GoModel provider | Type | Base URL | Serves LiteLLM model_name |\n| --- | --- | --- | --- |\n")
		for _, p := range r.Providers {
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", p.Name, p.Type, orDefault(p.BaseURL), codeList(p.ModelNames))
		}
	}
	if len(r.VirtualModels) > 0 {
		b.WriteString("\n## Virtual models\n\n| Model clients call | Strategy | Targets |\n| --- | --- | --- |\n")
		for _, vm := range r.VirtualModels {
			strategy := vm.Strategy
			if strategy == "" {
				strategy = "alias"
				if len(vm.Targets) > 1 {
					strategy = "round_robin"
				}
			}
			targets := make([]string, 0, len(vm.Targets))
			for _, t := range vm.Targets {
				label := "`" + t.qualified() + "`"
				if t.Weight > 0 {
					label += fmt.Sprintf(" (weight %g)", t.Weight)
				}
				targets = append(targets, label)
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", vm.Source, strategy, strings.Join(targets, ", "))
		}
	}
	if len(r.RequiredEnv) > 0 || len(r.WrittenEnv) > 0 {
		b.WriteString("\n## Environment\n\n")
		if len(r.RequiredEnv) > 0 {
			fmt.Fprintf(&b, "Set these for GoModel, as you did for LiteLLM: %s\n\n", codeList(r.RequiredEnv))
		}
		if len(r.WrittenEnv) > 0 {
			fmt.Fprintf(&b, "Inline values from the LiteLLM config were moved to `.env`: %s\n", codeList(r.WrittenEnv))
		}
	}
	r.writeFindings(&b, SeverityWarning, "Review before switching traffic")
	r.writeFindings(&b, SeveritySkipped, "Not migrated")
	r.writeFindings(&b, SeverityInfo, "Behavior changes")
	b.WriteString(nextSteps)
	return b.String()
}

func (r *Report) writeFindings(b *strings.Builder, severity Severity, title string) {
	if r.Count(severity) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n\n", title)
	for _, f := range r.Findings {
		if f.Severity == severity {
			fmt.Fprintf(b, "- `%s`: %s\n", f.Subject, f.Message)
		}
	}
}

const nextSteps = `
## Next steps

1. Review ` + "`config.yaml`" + ` and the sections above.
2. Start GoModel next to LiteLLM with the generated files and send a few test requests.
3. Point clients at GoModel. The base URL must end in ` + "`/v1`" + `
   (for example ` + "`http://gomodel:8080/v1`" + `).
4. Recreate API keys, teams, and budgets: they live in the LiteLLM database,
   not in config.yaml.

Guide: https://gomodel.enterpilot.io/docs/guides/migrate-from-litellm
`

func codeList(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = "`" + item + "`"
	}
	return strings.Join(quoted, ", ")
}

func orDefault(value string) string {
	if value == "" {
		return "default"
	}
	return "`" + value + "`"
}
