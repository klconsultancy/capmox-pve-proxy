{{- define "capmox-pve-proxy.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "capmox-pve-proxy.fullname" -}}
{{- if contains .Chart.Name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "capmox-pve-proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "capmox-pve-proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "capmox-pve-proxy.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "capmox-pve-proxy.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | trunc 63 | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* repository:tag, or repository:tag@digest: the digest decides, the tag documents. */}}
{{- define "capmox-pve-proxy.image" -}}
{{- $ref := printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) }}
{{- if .Values.image.digest }}
{{- $ref = printf "%s@%s" $ref .Values.image.digest }}
{{- end }}
{{- $ref }}
{{- end }}

{{/* The port's name, which is also the probes' scheme in lower case. */}}
{{- define "capmox-pve-proxy.portName" -}}
{{- ternary "http" "https" .Values.config.insecureHTTP }}
{{- end }}

{{/* Where the chart mounts what config.yaml refers to. */}}
{{- define "capmox-pve-proxy.configDir" -}}/etc/capmox-pve-proxy{{- end }}
{{- define "capmox-pve-proxy.tokensDir" -}}/secrets{{- end }}
{{- define "capmox-pve-proxy.tlsDir" -}}/tls{{- end }}
{{- define "capmox-pve-proxy.caDir" -}}/ca{{- end }}

{{/*
The proxy's config.yaml. Every path is one of the chart's mounts. A node listed twice
fails here, since its token's file would be ambiguous; the proxy refuses it too.
*/}}
{{- define "capmox-pve-proxy.config" -}}
{{- $tokens := include "capmox-pve-proxy.tokensDir" . }}
{{- $nodes := list }}
{{- $seen := dict }}
{{- range .Values.config.nodes }}
{{- if hasKey $seen .name }}
{{- fail (printf "config.nodes: node %q is listed twice" .name) }}
{{- end }}
{{- $_ := set $seen .name true }}
{{- $node := dict "name" .name "url" .url "tokenID" .tokenID "secretFile" (printf "%s/%s" $tokens .name) }}
{{- if .ca }}
{{- $_ := set $node "caFile" (printf "%s/%s.pem" (include "capmox-pve-proxy.caDir" $) .name) }}
{{- end }}
{{- $nodes = append $nodes $node }}
{{- end }}
{{- $config := dict
  "listen" ":8006"
  "clusterName" .Values.config.clusterName
  "client" (dict "tokenID" .Values.config.client.tokenID "secretFile" (printf "%s/client" $tokens))
  "nodes" $nodes }}
{{- if .Values.config.insecureHTTP }}
{{- $_ := set $config "insecureHTTP" true }}
{{- else }}
{{- $tls := include "capmox-pve-proxy.tlsDir" . }}
{{- $_ := set $config "tls" (dict "certFile" (printf "%s/tls.crt" $tls) "keyFile" (printf "%s/tls.key" $tls)) }}
{{- end }}
{{- toYaml $config }}
{{- end }}
