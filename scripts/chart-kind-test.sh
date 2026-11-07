#!/usr/bin/env bash
# Installs the Helm chart in the current Kubernetes context, such as a kind cluster, and
# waits until its pods are Ready: their /healthz probes pass only once the proxy has read
# the rendered config.yaml and every file it names from the chart's mounts. The nodes
# in ci/example-values.yaml cannot be reached, which /healthz does not ask about.
#
# It installs twice, in namespaces of their own: with a self-signed certificate, and
# with insecureHTTP. Each time it also calls the proxy through its Service, to check the
# certificate it serves and that it reads the client token from its Secret.
#
# Environment:
#   IMAGE_REPOSITORY  the proxy's image, already on the cluster's nodes
#   IMAGE_TAG         its tag
#
# Needs helm, kubectl, openssl and curl. Exits non-zero if any check fails.
set -Eeuo pipefail

: "${IMAGE_REPOSITORY:?}" "${IMAGE_TAG:?}"
cd "$(dirname "$0")/.."
CHART=charts/capmox-pve-proxy
RELEASE=ci
SERVICE=$RELEASE-capmox-pve-proxy
CLIENT_TOKEN_ID='capmox@pve!capmox' # as in ci/example-values.yaml
CLIENT_SECRET=client-secret
WORK=$(mktemp -d)
PORT_FORWARD=""
NAMESPACE=""

cleanup() {
	if [[ -n $PORT_FORWARD ]]; then
		kill "$PORT_FORWARD" 2>/dev/null || true
	fi
	rm -rf "$WORK"
}
trap cleanup EXIT

# On a failure, what the pods were doing.
diagnose() {
	[[ -n $NAMESPACE ]] || return 0
	kubectl -n "$NAMESPACE" get all,configmap,secret || true
	kubectl -n "$NAMESPACE" describe pods || true
	kubectl -n "$NAMESPACE" logs -l app.kubernetes.io/instance=$RELEASE --all-containers --tail=50 || true
}
trap diagnose ERR

# install NAMESPACE SCHEME [helm arguments...] installs the chart and calls it.
install() {
	NAMESPACE=$1
	local scheme=$2
	shift 2
	local host=$SERVICE.$NAMESPACE.svc
	echo "== $NAMESPACE"
	kubectl create namespace "$NAMESPACE"

	# A self-signed certificate for the Service's name. It is also both nodes' CA,
	# one from a ConfigMap and one from the Secret: the proxy only reads them.
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
		-subj "/CN=$host" -addext "subjectAltName=DNS:$host" \
		-keyout "$WORK/tls.key" -out "$WORK/tls.crt" 2>/dev/null
	kubectl -n "$NAMESPACE" create secret generic capmox-pve-proxy \
		--from-literal=client=$CLIENT_SECRET \
		--from-literal=pve01=pve01-secret \
		--from-literal=pve02=pve02-secret \
		--from-file=tls.crt="$WORK/tls.crt" \
		--from-file=tls.key="$WORK/tls.key" \
		--from-file=pve02.pem="$WORK/tls.crt"
	kubectl -n "$NAMESPACE" create configmap pve01-ca --from-file=pve-root-ca.pem="$WORK/tls.crt"

	helm install "$RELEASE" "$CHART" --namespace "$NAMESPACE" \
		-f "$CHART/ci/example-values.yaml" "$@" \
		--set image.repository="$IMAGE_REPOSITORY" \
		--set image.tag="$IMAGE_TAG" \
		--set image.pullPolicy=Never \
		--wait --timeout 3m
	kubectl -n "$NAMESPACE" rollout status deployment/"$SERVICE" --timeout 1m
	echo "ok    $NAMESPACE: pods Ready"

	kubectl -n "$NAMESPACE" port-forward service/"$SERVICE" 18006:8006 >/dev/null &
	PORT_FORWARD=$!
	local curl=(curl -sS --max-time 10 --resolve "$host:18006:127.0.0.1" --cacert "$WORK/tls.crt")
	local base=$scheme://$host:18006
	for _ in $(seq 30); do
		"${curl[@]}" -o /dev/null "$base/healthz" 2>/dev/null && break
		sleep 1
	done
	local body status
	body=$("${curl[@]}" "$base/healthz")
	[[ $body == ok ]] || { echo "FAIL  /healthz: $body"; return 1; }
	echo "ok    $NAMESPACE: /healthz through the Service, over $scheme"
	status=$("${curl[@]}" -o /dev/null -w '%{http_code}' "$base/api2/json/version")
	[[ $status == 401 ]] || { echo "FAIL  /version without a token: HTTP $status, want 401"; return 1; }
	# The nodes cannot be reached, so the answer is an error, but not the token's.
	status=$("${curl[@]}" -o /dev/null -w '%{http_code}' \
		-H "Authorization: PVEAPIToken=$CLIENT_TOKEN_ID=$CLIENT_SECRET" "$base/api2/json/version")
	[[ $status != 401 ]] || { echo "FAIL  /version with the client token: HTTP 401"; return 1; }
	echo "ok    $NAMESPACE: the client token is accepted (HTTP $status)"

	kill "$PORT_FORWARD"
	wait "$PORT_FORWARD" 2>/dev/null || true
	PORT_FORWARD=""
}

install capmox-pve-proxy-tls https
install capmox-pve-proxy-insecure http -f "$CHART/ci/insecure-values.yaml"
