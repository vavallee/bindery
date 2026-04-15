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
