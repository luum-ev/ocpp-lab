{{- define "ocpp-lab.fullname" -}}
{{- printf "%s" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ocpp-lab.labels" -}}
app.kubernetes.io/name: ocpp-lab
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "ocpp-lab.selectorLabels" -}}
app.kubernetes.io/name: ocpp-lab
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
