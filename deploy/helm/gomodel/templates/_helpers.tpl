{{/*
Expand the name of the chart.
*/}}
{{- define "gomodel.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
Truncated to 63 chars because some Kubernetes name fields are limited to this.
*/}}
{{- define "gomodel.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "gomodel.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "gomodel.labels" -}}
helm.sh/chart: {{ include "gomodel.chart" . }}
{{ include "gomodel.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: gomodel
{{- end }}

{{/*
Selector labels
*/}}
{{- define "gomodel.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gomodel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
The image reference, defaulting the tag to the chart appVersion.
*/}}
{{- define "gomodel.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end }}

{{/*
Create the name of the service account to use.
*/}}
{{- define "gomodel.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "gomodel.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret to reference via envFrom. Prefers an existing secret.
*/}}
{{- define "gomodel.secretName" -}}
{{- if .Values.secrets.existingSecret }}
{{- .Values.secrets.existingSecret }}
{{- else }}
{{- include "gomodel.fullname" . }}
{{- end }}
{{- end }}

{{/*
Whether the chart manages its own Secret (i.e. no existing secret and some data set).
*/}}
{{- define "gomodel.createSecret" -}}
{{- if and (not .Values.secrets.existingSecret) (or .Values.secrets.masterKey .Values.secrets.data) -}}
true
{{- end -}}
{{- end }}

{{/*
Whether persistent (SQLite/StatefulSet) mode is active.
*/}}
{{- define "gomodel.persistent" -}}
{{- if .Values.persistence.enabled -}}
true
{{- end -}}
{{- end }}

{{/*
Effective replica count. Forced to 1 in persistent (SQLite) mode.
*/}}
{{- define "gomodel.replicaCount" -}}
{{- if .Values.persistence.enabled -}}
1
{{- else -}}
{{- .Values.replicaCount -}}
{{- end -}}
{{- end }}

{{/*
The URL path prefix (BASE_PATH) used to build probe paths. Honors an env override,
then config.server.base_path, defaulting to "/".
*/}}
{{- define "gomodel.basePath" -}}
{{- $bp := "/" -}}
{{- if and .Values.config .Values.config.server .Values.config.server.base_path -}}
{{- $bp = .Values.config.server.base_path -}}
{{- end -}}
{{- if and .Values.env .Values.env.BASE_PATH -}}
{{- $bp = .Values.env.BASE_PATH -}}
{{- end -}}
{{- $bp = printf "/%s" (trimPrefix "/" (trimSuffix "/" $bp)) -}}
{{- if eq $bp "/" -}}{{- $bp = "" -}}{{- end -}}
{{- $bp -}}
{{- end }}

{{/*
Validation guardrails.
*/}}
{{- define "gomodel.validate" -}}
{{- if and .Values.persistence.enabled (gt (int .Values.replicaCount) 1) -}}
{{- fail "persistence.enabled=true uses SQLite storage which cannot be shared across pods. Set replicaCount to 1, or disable persistence and use an external database." -}}
{{- end -}}
{{- if and .Values.persistence.enabled .Values.autoscaling.enabled -}}
{{- fail "autoscaling is incompatible with persistence.enabled=true (SQLite single-writer). Disable one of them." -}}
{{- end -}}
{{- end }}
