{{- define "openspoor.name" -}}{{ .Release.Name }}-openspoor{{- end -}}
{{- define "openspoor.image" -}}{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}{{- end -}}
{{- define "openspoor.labels" -}}
app.kubernetes.io/name: openspoor
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}
{{- define "openspoor.env" -}}
- name: OPENSPOOR_MODE
  value: {{ .Values.mode | quote }}
- name: OPENSPOOR_PUBLIC_URL
  value: {{ .Values.publicUrl | quote }}
- name: OPENSPOOR_BASE_DOMAIN
  value: {{ .Values.baseDomain | quote }}
{{- range $k, $v := .Values.extraEnv }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end }}
{{- end -}}
