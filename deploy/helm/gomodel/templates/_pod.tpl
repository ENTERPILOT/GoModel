{{/*
Environment variable list shared by all workloads.
*/}}
{{- define "gomodel.env" -}}
{{- if or .Values.metrics.enabled .Values.metrics.serviceMonitor.enabled }}
- name: METRICS_ENABLED
  value: "true"
{{- end }}
{{- range $key, $value := .Values.env }}
- name: {{ $key }}
  value: {{ $value | quote }}
{{- end }}
{{- with .Values.extraEnv }}
{{- toYaml . | nindent 0 }}
{{- end }}
{{- end }}

{{/*
Volumes shared by all workloads. In persistent mode the "data" volume is provided by
the StatefulSet volumeClaimTemplate (or existingClaim) and is not declared here.
*/}}
{{- define "gomodel.volumes" -}}
- name: cache
  emptyDir: {}
{{- if not (include "gomodel.persistent" .) }}
- name: data
  emptyDir: {}
{{- else if .Values.persistence.existingClaim }}
- name: data
  persistentVolumeClaim:
    claimName: {{ .Values.persistence.existingClaim }}
{{- end }}
{{- if .Values.config }}
- name: config
  configMap:
    name: {{ include "gomodel.fullname" . }}
{{- end }}
{{- with .Values.extraVolumes }}
{{- toYaml . | nindent 0 }}
{{- end }}
{{- end }}

{{/*
The GoModel container spec.
*/}}
{{- define "gomodel.container" -}}
- name: {{ .Chart.Name }}
  image: {{ include "gomodel.image" . }}
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  {{- with .Values.securityContext }}
  securityContext:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  ports:
    - name: http
      containerPort: 8080
      protocol: TCP
  {{- $env := include "gomodel.env" . | trim }}
  {{- if $env }}
  env:
    {{- $env | nindent 4 }}
  {{- end }}
  {{- if or (eq (include "gomodel.createSecret" .) "true") .Values.secrets.existingSecret }}
  envFrom:
    - secretRef:
        name: {{ include "gomodel.secretName" . }}
  {{- end }}
  {{- if .Values.livenessProbe.enabled }}
  livenessProbe:
    httpGet:
      path: {{ include "gomodel.basePath" . }}/health
      port: http
    initialDelaySeconds: {{ .Values.livenessProbe.initialDelaySeconds }}
    periodSeconds: {{ .Values.livenessProbe.periodSeconds }}
    timeoutSeconds: {{ .Values.livenessProbe.timeoutSeconds }}
    failureThreshold: {{ .Values.livenessProbe.failureThreshold }}
    successThreshold: {{ .Values.livenessProbe.successThreshold }}
  {{- end }}
  {{- if .Values.readinessProbe.enabled }}
  readinessProbe:
    httpGet:
      path: {{ include "gomodel.basePath" . }}/health/ready
      port: http
    initialDelaySeconds: {{ .Values.readinessProbe.initialDelaySeconds }}
    periodSeconds: {{ .Values.readinessProbe.periodSeconds }}
    timeoutSeconds: {{ .Values.readinessProbe.timeoutSeconds }}
    failureThreshold: {{ .Values.readinessProbe.failureThreshold }}
    successThreshold: {{ .Values.readinessProbe.successThreshold }}
  {{- end }}
  {{- if .Values.startupProbe.enabled }}
  startupProbe:
    httpGet:
      path: {{ include "gomodel.basePath" . }}/health
      port: http
    periodSeconds: {{ .Values.startupProbe.periodSeconds }}
    timeoutSeconds: {{ .Values.startupProbe.timeoutSeconds }}
    failureThreshold: {{ .Values.startupProbe.failureThreshold }}
    successThreshold: {{ .Values.startupProbe.successThreshold }}
  {{- end }}
  {{- with .Values.resources }}
  resources:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  volumeMounts:
    - name: cache
      mountPath: /app/.cache
    - name: data
      mountPath: /app/data
    {{- if .Values.config }}
    - name: config
      mountPath: /app/config/config.yaml
      subPath: config.yaml
      readOnly: true
    {{- end }}
    {{- with .Values.extraVolumeMounts }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
{{- end }}

{{/*
Pod template metadata annotations, including config/secret checksums for auto-reload.
*/}}
{{- define "gomodel.podAnnotations" -}}
{{- if .Values.config }}
checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
{{- end }}
{{- if eq (include "gomodel.createSecret" .) "true" }}
checksum/secret: {{ include (print $.Template.BasePath "/secret.yaml") . | sha256sum }}
{{- end }}
{{- with .Values.podAnnotations }}
{{- toYaml . | nindent 0 }}
{{- end }}
{{- end }}

{{/*
Shared pod spec body (everything under spec.template.spec).
*/}}
{{- define "gomodel.podSpec" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
serviceAccountName: {{ include "gomodel.serviceAccountName" . }}
automountServiceAccountToken: {{ .Values.serviceAccount.automount }}
{{- with .Values.podSecurityContext }}
securityContext:
  {{- toYaml . | nindent 2 }}
{{- end }}
terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
containers:
  {{- include "gomodel.container" . | nindent 2 }}
volumes:
  {{- include "gomodel.volumes" . | nindent 2 }}
{{- with .Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.topologySpreadConstraints }}
topologySpreadConstraints:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}
