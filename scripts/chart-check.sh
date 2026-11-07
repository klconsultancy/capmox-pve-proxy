#!/usr/bin/env bash
# Static checks of the Helm chart, as CI runs them: helm lint, the rendered manifests
# through kubeconform, and each values file in ci/invalid/ refused for its reason.
#
# Each invalid file is applied on top of ci/example-values.yaml, so it is refused for
# what it changes, and its first line, "# refused: <text>", is what the error must say.
#
# Needs helm and kubeconform. Exits non-zero if any check fails.
set -euo pipefail

cd "$(dirname "$0")/.."
CHART=charts/capmox-pve-proxy
EXAMPLE=$CHART/ci/example-values.yaml
INSECURE=$CHART/ci/insecure-values.yaml
FAILURES=0

ok() { echo "ok    $*"; }
fail() {
	echo "FAIL  $*"
	FAILURES=$((FAILURES + 1))
}

# check NAME KINDS [helm template arguments...] renders the chart, validates it and
# checks the kinds it renders and the paths in its config.yaml.
check() {
	local name=$1 want=$2
	shift 2
	local out kinds paths
	if ! out=$(helm template ci "$CHART" -f "$EXAMPLE" "$@" 2>&1); then
		fail "$name: helm template: $out"
		return
	fi
	if kubeconform -strict -summary <<<"$out"; then
		ok "$name: kubeconform"
	else
		fail "$name: kubeconform"
	fi
	kinds=$(sed -n 's/^kind: //p' <<<"$out" | sort | xargs)
	if [[ $kinds == "$want" ]]; then
		ok "$name: renders $kinds"
	else
		fail "$name: renders $kinds, want $want"
	fi
	# Every file config.yaml names must be under one of the chart's mounts.
	paths=$(grep -E '^ +-? ?(secretFile|certFile|keyFile|caFile): ' <<<"$out" | awk '{print $NF}')
	if bad=$(grep -vE '^/(secrets|tls|ca)/[^/]+$' <<<"$paths"); then
		fail "$name: paths outside the chart's mounts: $bad"
	else
		ok "$name: config.yaml paths: $(xargs <<<"$paths")"
	fi
}

helm lint --strict "$CHART" -f "$EXAMPLE"
helm lint --strict "$CHART" -f "$EXAMPLE" -f "$INSECURE"

check example "ConfigMap Deployment PodDisruptionBudget Service"
check networkpolicy "ConfigMap Deployment NetworkPolicy PodDisruptionBudget Service" \
	--set networkPolicy.enabled=true
check insecure "ConfigMap Deployment NetworkPolicy PodDisruptionBudget Service" \
	-f "$INSECURE" --set networkPolicy.enabled=true

for file in "$CHART"/ci/invalid/*.yaml; do
	want=$(sed -n '1s/^# refused: //p' "$file")
	if [[ -z $want ]]; then
		fail "$file: no \"# refused:\" line"
		continue
	fi
	if out=$(helm template ci "$CHART" -f "$EXAMPLE" -f "$file" 2>&1); then
		fail "$file: accepted"
	elif [[ $out != *"$want"* ]]; then
		fail "$file: refused, but not for \"$want\": $out"
	else
		ok "$file: refused ($want)"
	fi
done

if ((FAILURES > 0)); then
	echo "$FAILURES check(s) failed"
	exit 1
fi
