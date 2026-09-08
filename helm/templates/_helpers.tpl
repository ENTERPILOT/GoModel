{{/*
Expand the name of the chart.
*/}}
{{- define "gomodel.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name, truncated to the 63 character DNS limit.
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

{{- define "gomodel.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "gomodel.labels" -}}
helm.sh/chart: {{ include "gomodel.chart" . }}
{{ include "gomodel.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | trunc 63 | trimAll "-_." | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "gomodel.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gomodel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "gomodel.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "gomodel.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "gomodel.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) }}
{{- end }}

{{/*
Validate value combinations that would produce a broken deployment.
*/}}
{{- define "gomodel.validate" -}}
{{- range $key := list "auth" "providers" "redis" "cache" "gateway" "server" "logging" }}
{{- if hasKey $.Values $key }}
{{- fail (printf "%s is a chart 0.1.x value that this chart no longer reads; migrate it to env / secretEnv / extraEnvFrom as described in the chart README before upgrading" $key) }}
{{- end }}
{{- end }}
{{- range $key := list "PORT" "STORAGE_TYPE" "METRICS_ENABLED" }}
{{- if or (hasKey $.Values.env $key) (hasKey $.Values.secretEnv $key) }}
{{- fail (printf "%s is set by the chart; use storage.type, metrics.enabled, or the fixed container port instead of env.%s" $key $key) }}
{{- end }}
{{- end }}
{{- $type := .Values.storage.type }}
{{- if not (has $type (list "sqlite" "postgresql" "mongodb")) }}
{{- fail (printf "storage.type must be sqlite, postgresql, or mongodb (got %q)" $type) }}
{{- end }}
{{- if and (eq $type "sqlite") (or (gt (int .Values.replicaCount) 1) .Values.autoscaling.enabled) }}
{{- fail "SQLite storage is per pod: set storage.type to postgresql or mongodb before running more than one replica" }}
{{- end }}
{{- if ne $type "sqlite" }}
{{- $urlEnv := include "gomodel.storageUrlEnv" . }}
{{- $fromExtraEnv := false }}
{{- range .Values.extraEnv }}{{ if eq .name $urlEnv }}{{ $fromExtraEnv = true }}{{ end }}{{ end }}
{{- if not (or .Values.storage.url .Values.extraEnvFrom $fromExtraEnv (hasKey .Values.secretEnv $urlEnv)) }}
{{- fail (printf "storage.url is required for storage.type %s (or supply %s through secretEnv, extraEnv, or extraEnvFrom)" $type $urlEnv) }}
{{- end }}
{{- end }}
{{- if and .Values.config .Values.existingConfigMap }}
{{- fail "set either config or existingConfigMap, not both" }}
{{- end }}
{{- if and .Values.httpRoute.enabled (not .Values.httpRoute.parentRefs) }}
{{- fail "httpRoute.parentRefs is required when httpRoute.enabled is true" }}
{{- end }}
{{- end }}

{{/*
Environment variable that carries the storage connection URL for the selected backend.
*/}}
{{- define "gomodel.storageUrlEnv" -}}
{{- if eq .Values.storage.type "postgresql" }}POSTGRES_URL{{ else if eq .Values.storage.type "mongodb" }}MONGODB_URL{{ end }}
{{- end }}

{{/*
True when SQLite data is kept on a PersistentVolumeClaim.
*/}}
{{- define "gomodel.usesPVC" -}}
{{- if and (eq .Values.storage.type "sqlite") .Values.persistence.enabled }}true{{ end }}
{{- end }}

{{/*
Name of the chart-managed Secret; empty when there is nothing to store.
*/}}
{{- define "gomodel.secretName" -}}
{{- if or .Values.secretEnv .Values.storage.url }}{{ include "gomodel.fullname" . }}{{ end }}
{{- end }}

{{/*
Name of the ConfigMap holding config.yaml; empty when no file is configured.
*/}}
{{- define "gomodel.configMapName" -}}
{{- if .Values.existingConfigMap }}{{ .Values.existingConfigMap }}{{ else if .Values.config }}{{ include "gomodel.fullname" . }}-config{{ end }}
{{- end }}

{{/*
Prefix an application path with env.BASE_PATH so probes and scrapes keep working
when the gateway is mounted under a prefix. Mirrors the server's
NormalizeBasePath: trimmed, a leading slash added, then path-cleaned, with root
rendering as no prefix at all.
*/}}
{{- define "gomodel.pathWithBasePath" -}}
{{- $base := "" -}}
{{- with trim (toString (default "" .root.Values.env.BASE_PATH)) -}}
{{- $base = clean (printf "/%s" (trimPrefix "/" .)) -}}
{{- if eq $base "/" -}}{{- $base = "" -}}{{- end -}}
{{- end -}}
{{- printf "%s%s" $base .path -}}
{{- end }}
