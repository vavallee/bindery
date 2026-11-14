{{- define "bindery.name" -}}
bindery
{{- end }}

{{- define "bindery.fullname" -}}
bindery
{{- end }}

{{- define "bindery.labels" -}}
app.kubernetes.io/name: {{ include "bindery.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "bindery.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bindery.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "bindery.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
  {{- default (include "bindery.fullname" .) .Values.serviceAccount.name }}
{{- else -}}
  {{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
The scrape path for /metrics: metrics.serviceMonitor.path when set, otherwise
/metrics under env.BINDERY_URL_BASE.
*/}}
{{- define "bindery.metricsPath" -}}
{{- if .Values.metrics.serviceMonitor.path -}}
{{- .Values.metrics.serviceMonitor.path -}}
{{- else -}}
{{- printf "%s/metrics" (trimSuffix "/" (default "" .Values.env.BINDERY_URL_BASE)) -}}
{{- end -}}
{{- end }}

{{/*
PromQL label matchers that pick this release's series. The ServiceMonitor
names the job after the Service.
*/}}
{{- define "bindery.metricsSelector" -}}
job="{{ include "bindery.fullname" . }}",namespace="{{ .Release.Namespace }}"
{{- end }}
