#!/usr/bin/env bash
# Smoke test of a running capmox-pve-proxy against real Proxmox VE nodes: step 1 of
# docs/testing.md. It calls the proxy the way CAPMOX does and checks each answer.
#
# Environment:
#   PROXY_URL          the proxy, for example https://10.0.0.10:8006
#   PROXY_TOKEN_ID     the client token CAPMOX sends, for example capmox@pve!capmox
#   PROXY_SECRET_FILE  the file holding that token's secret
#   PROXY_CA           optional: the CA file that signed the proxy's certificate
#   TEMPLATE_VMID      the template every node carries under the same VMID
#   CLONE=1            also clone the template from the first node onto the last,
#                      then delete the clone (needs at least two nodes)
#   CLONE_POOL         optional: the pool the clone goes into, as CAPMOX's pool setting
#   CLONE_STORAGE      optional: the storage the clone goes to
#   TASK_TIMEOUT       optional: seconds to wait for a clone or delete (default 600)
#
# Needs curl and jq. Exits non-zero if any check fails.
set -euo pipefail

: "${PROXY_URL:?}" "${PROXY_TOKEN_ID:?}" "${PROXY_SECRET_FILE:?}" "${TEMPLATE_VMID:?}"
TASK_TIMEOUT=${TASK_TIMEOUT:-600}
API="${PROXY_URL%/}/api2/json"
AUTH="PVEAPIToken=${PROXY_TOKEN_ID}=$(<"$PROXY_SECRET_FILE")"
CURL=(curl -sS --max-time 30)
if [[ -n ${PROXY_CA:-} ]]; then
	CURL+=(--cacert "$PROXY_CA")
fi

BODY_FILE=$(mktemp)
FAILURES=0
CLONED="" # node/vmid of a clone not yet deleted, removed on exit
STATUS=""
BODY=""
NODES=()

cleanup() {
	if [[ -n $CLONED ]]; then
		echo "Deleting the clone ${CLONED#*/} on ${CLONED%/*} left behind by a failed check"
		api DELETE "/nodes/${CLONED%/*}/qemu/${CLONED#*/}" || true
	fi
	rm -f "$BODY_FILE"
}
trap cleanup EXIT

ok() { echo "ok    $*"; }
fail() {
	echo "FAIL  $*"
	FAILURES=$((FAILURES + 1))
}

# api METHOD PATH [curl arguments...] sets STATUS and BODY.
api() {
	local method=$1 path=$2
	shift 2
	STATUS=$("${CURL[@]}" -o "$BODY_FILE" -w '%{http_code}' -X "$method" \
		-H "Authorization: ${AUTH_OVERRIDE:-$AUTH}" "$@" "$API$path") || STATUS=000
	BODY=$(<"$BODY_FILE")
}

# expect STATUS DESCRIPTION reports whether the last call answered with STATUS.
expect() {
	if [[ $STATUS == "$1" ]]; then
		ok "$2"
		return 0
	fi
	fail "$2: HTTP $STATUS, expected $1: $BODY"
	return 1
}

data() { jq -r "$1" <<<"$BODY"; }

# wait_task NODE UPID waits for a task to stop and reports whether it ended OK.
wait_task() {
	local node=$1 upid=$2 deadline=$((SECONDS + TASK_TIMEOUT))
	local path
	path="/nodes/$node/tasks/$(jq -rn --arg u "$upid" '$u|@uri')/status"
	while ((SECONDS < deadline)); do
		api GET "$path"
		if [[ $STATUS != 200 ]]; then
			fail "task $upid: HTTP $STATUS: $BODY"
			return 1
		fi
		if [[ $(data .data.status) == stopped ]]; then
			if [[ $(data .data.exitstatus) == OK ]]; then
				return 0
			fi
			fail "task $upid ended with: $(data .data.exitstatus)"
			return 1
		fi
		sleep 2
	done
	fail "task $upid still running after ${TASK_TIMEOUT}s"
	return 1
}

echo "== Authentication and refusals"
AUTH_OVERRIDE="PVEAPIToken=${PROXY_TOKEN_ID}=wrong" api GET /version
expect 401 "a wrong client token is refused" || true
api GET /cluster/options
expect 501 "an endpoint the proxy does not implement is refused" || true

echo "== Cluster"
api GET /version
expect 200 "/version answers: $(data '.data.version // empty')" || true

api GET /cluster/status
if expect 200 "/cluster/status answers"; then
	if [[ $(data '[.data[] | select(.type == "cluster" and .quorate == 1)] | length') == 1 ]]; then
		ok "the cluster is quorate: $(data '.data[] | select(.type == "cluster") | .name')"
	else
		fail "no quorate cluster entry: $BODY"
	fi
	# Not mapfile: macOS ships bash 3.2.
	while read -r node; do NODES+=("$node"); done < <(data '.data[] | select(.type == "node") | .name')
	offline=$(data '[.data[] | select(.type == "node" and .online != 1) | .name] | join(" ")')
	if [[ -z $offline ]]; then
		ok "every node is online: ${NODES[*]}"
	else
		fail "nodes offline: $offline (the proxy log says why)"
	fi
fi
if ((${#NODES[@]} == 0)); then
	echo "No nodes to test; stopping."
	exit 1
fi

api GET '/cluster/resources?type=vm'
if expect 200 "/cluster/resources answers"; then
	reported=$(data "[.data[] | select(.vmid == $TEMPLATE_VMID)]")
	count=$(jq length <<<"$reported")
	on=$(jq -r '.[0].node // empty' <<<"$reported")
	if [[ $count == 1 && $(jq -r '.[0].template' <<<"$reported") == 1 ]]; then
		ok "template $TEMPLATE_VMID is reported once, on $on"
	else
		fail "template $TEMPLATE_VMID is reported $count times: $reported"
	fi
	if [[ -n $on && $on != "${NODES[0]}" ]]; then
		fail "template $TEMPLATE_VMID is reported on $on, not on the first node ${NODES[0]}"
	fi
fi

echo "== Nodes"
for node in "${NODES[@]}"; do
	api GET "/nodes/$node/qemu/$TEMPLATE_VMID/config"
	if expect 200 "$node: template $TEMPLATE_VMID's config"; then
		if [[ $(data .data.template) == 1 ]]; then
			ok "$node: VMID $TEMPLATE_VMID is a template, tags: $(data '.data.tags // "none"')"
		else
			fail "$node: VMID $TEMPLATE_VMID is not a template"
		fi
	fi
	api GET "/nodes/$node/status"
	if expect 200 "$node: status"; then
		if [[ $(data .data.memory.total) -gt 0 ]]; then
			ok "$node: reports its memory, so the scheduler can place machines on it"
		else
			fail "$node: reports no memory, as the proxy does for a node that is down"
		fi
	fi
	api GET "/nodes/$node/qemu"
	expect 200 "$node: guests" || true
	api GET "/nodes/$node/storage"
	expect 200 "$node: storage: $(data '[.data[]?.storage] | join(", ")')" || true
done

echo "== VMIDs"
api GET /cluster/nextid
NEXTID=""
if expect 200 "/cluster/nextid answers: $(data .data)"; then
	NEXTID=$(data .data)
fi
api GET "/cluster/nextid?vmid=$TEMPLATE_VMID"
expect 400 "VMID $TEMPLATE_VMID counts as taken" || true

if [[ ${CLONE:-} != 1 ]]; then
	echo "== Clone skipped: set CLONE=1 to clone the template across nodes"
elif ((${#NODES[@]} < 2)); then
	fail "a clone across nodes needs at least two nodes"
elif [[ -z $NEXTID ]]; then
	fail "no VMID for the clone"
else
	src=${NODES[0]} dst=${NODES[${#NODES[@]} - 1]}
	echo "== Clone of template $TEMPLATE_VMID from $src onto $dst as VMID $NEXTID"
	params=(--data-urlencode "newid=$NEXTID" --data-urlencode "name=capmox-pve-proxy-smoke-$NEXTID"
		--data-urlencode "target=$dst" --data-urlencode "full=1")
	if [[ -n ${CLONE_POOL:-} ]]; then
		params+=(--data-urlencode "pool=$CLONE_POOL")
	fi
	if [[ -n ${CLONE_STORAGE:-} ]]; then
		params+=(--data-urlencode "storage=$CLONE_STORAGE")
	fi
	api POST "/nodes/$src/qemu/$TEMPLATE_VMID/clone" "${params[@]}"
	if expect 200 "the clone starts"; then
		CLONED="$dst/$NEXTID"
		upid=$(data .data)
		if [[ $(cut -d: -f2 <<<"$upid") == "$dst" ]]; then
			ok "the clone runs on $dst: $upid"
		else
			fail "the clone runs on another node than $dst: $upid"
		fi
		if wait_task "$dst" "$upid"; then
			ok "the clone finished"
			api GET "/nodes/$dst/qemu/$NEXTID/status/current"
			expect 200 "VM $NEXTID exists on $dst" || true
			api GET "/cluster/nextid?vmid=$NEXTID"
			expect 400 "VMID $NEXTID counts as taken now" || true

			api DELETE "/nodes/$dst/qemu/$NEXTID"
			if expect 200 "the delete of VM $NEXTID starts" && wait_task "$dst" "$(data .data)"; then
				CLONED=""
				ok "VM $NEXTID is deleted"
				api GET "/cluster/nextid?vmid=$NEXTID"
				expect 200 "VMID $NEXTID counts as free again" || true
			fi
		fi
	fi
fi

echo
if ((FAILURES > 0)); then
	echo "$FAILURES check(s) failed"
	exit 1
fi
echo "All checks passed"
