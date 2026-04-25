{{/*
Expand the name of the chart.
*/}}
{{- define "gomodel.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
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
{{- end }}

{{/*
Selector labels
*/}}
{{- define "gomodel.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gomodel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the secret containing provider API keys
*/}}
{{- define "gomodel.providerSecretName" -}}
{{- if .Values.providers.existingSecret }}
{{- .Values.providers.existingSecret }}
{{- else }}
{{- include "gomodel.fullname" . }}-providers
{{- end }}
{{- end }}

{{/*
Create the name of the secret containing auth credentials
*/}}
{{- define "gomodel.authSecretName" -}}
{{- if .Values.auth.existingSecret }}
{{- .Values.auth.existingSecret }}
{{- else }}
{{- include "gomodel.fullname" . }}-auth
{{- end }}
{{- end }}

{{/*
Determine the Redis URL - either from values or auto-generated for subchart
*/}}
{{- define "gomodel.redisUrl" -}}
{{- if .Values.cache.redis.url }}
{{- .Values.cache.redis.url }}
{{- else if .Values.redis.enabled }}
{{- printf "redis://%s-redis-master:6379" .Release.Name }}
{{- else }}
{{- "" }}
{{- end }}
{{- end }}

{{/*
Create the image reference
*/}}
{{- define "gomodel.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

{{/*
Normalize the public base path used by the application.
*/}}
{{- define "gomodel.basePath" -}}
{{- $basePath := trim (default "/" .Values.server.basePath) -}}
{{- if or (eq $basePath "") (eq $basePath "/") -}}
/
{{- else -}}
{{- if not (hasPrefix "/" $basePath) -}}
{{- $basePath = printf "/%s" $basePath -}}
{{- end -}}
{{- $basePath = clean $basePath -}}
{{- if or (eq $basePath ".") (eq $basePath "/") -}}
/
{{- else -}}
{{- $basePath -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Prefix an application path with server.basePath unless it is already prefixed.
*/}}
{{- define "gomodel.pathWithBasePath" -}}
{{- $root := .root -}}
{{- $path := trim (default "/" .path) -}}
{{- if or (eq $path "") (eq $path "/") -}}
{{- $path = "/" -}}
{{- else if not (hasPrefix "/" $path) -}}
{{- $path = printf "/%s" $path -}}
{{- end -}}
{{- $basePath := include "gomodel.basePath" $root -}}
{{- if eq $path "/" -}}
{{- if eq $basePath "/" -}}
{{- $path -}}
{{- else -}}
{{- $basePath -}}
{{- end -}}
{{- else if eq $basePath "/" -}}
{{- $path -}}
{{- else if or (eq $path $basePath) (hasPrefix (printf "%s/" $basePath) $path) -}}
{{- $path -}}
{{- else -}}
{{- printf "%s%s" $basePath $path -}}
{{- end -}}
{{- end }}

{{/*
Generate provider API key entries for the Secret stringData.
*/}}
{{- define "gomodel.providerSecretData" -}}
{{- range $name, $config := .Values.providers }}
  {{- if and (kindIs "map" $config) (hasKey $config "apiKey") $config.apiKey }}
{{ upper $name }}_API_KEY: {{ $config.apiKey | quote }}
  {{- end }}
{{- end }}
{{- end }}

{{/*
Generate provider environment variables for the Deployment.
*/}}
{{- define "gomodel.providerEnvVars" -}}
{{- $secretName := include "gomodel.providerSecretName" . -}}
{{- range $name, $config := .Values.providers }}
{{- if kindIs "map" $config }}
{{- $hasAPIKey := and (hasKey $config "apiKey") $config.apiKey }}
{{- $enabledWithExistingSecret := and $.Values.providers.existingSecret (hasKey $config "enabled") $config.enabled }}
{{- $enabledWithBaseURL := and (hasKey $config "enabled") $config.enabled $config.baseUrl }}
{{- if or $hasAPIKey $enabledWithExistingSecret }}
- name: {{ upper $name }}_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $secretName }}
      key: {{ upper $name }}_API_KEY
{{- end }}
{{- if or (or $hasAPIKey $enabledWithExistingSecret) $enabledWithBaseURL }}
{{- if $config.baseUrl }}
- name: {{ upper $name }}_BASE_URL
  value: {{ $config.baseUrl | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
