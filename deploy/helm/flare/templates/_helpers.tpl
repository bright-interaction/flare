{{- define "flare.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "flare.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "flare.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "flare.labels" -}}
app.kubernetes.io/name: {{ include "flare.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "flare.selectorLabels" -}}
app.kubernetes.io/name: {{ include "flare.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "flare.secretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{ .Values.secrets.existingSecret }}
{{- else -}}
{{ include "flare.fullname" . }}
{{- end -}}
{{- end -}}

{{/* The Postgres DATABASE_URL: bundled service or external. */}}
{{- define "flare.databaseURL" -}}
{{- if .Values.postgres.bundled -}}
postgres://flare:{{ required "postgres.password is required when postgres.bundled=true and you are not supplying secrets.existingSecret (the chart ships no default; set a strong value). NOTE: on an EXISTING install this does not rotate the database password, which was fixed at initdb; change it in Postgres first with ALTER USER flare PASSWORD ..." .Values.postgres.password }}@{{ include "flare.fullname" . }}-db:5432/flare?sslmode=disable
{{- else -}}
{{ required "postgres.externalUrl is required when postgres.bundled=false" .Values.postgres.externalUrl }}
{{- end -}}
{{- end -}}
