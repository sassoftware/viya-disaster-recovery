#!/usr/bin/env bash
# Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

# =============================================================================
# update_contour_httpproxy.sh
#
# Purpose : Run MANUALLY after verifying the Velero restore is healthy.
#           Updates the Contour `sas-httpproxy-root` HTTPProxy on the restored
#           (CLUSTER_TYPE=restore) cluster so external access is routed
#           through the restored cluster DNS.
#
#           1. Loads environment.properties (KUBECONFIG_PATH, CLUSTER_TYPE).
#           2. Validates CLUSTER_TYPE=restore.
#           3. Exports KUBECONFIG and validates cluster connectivity.
#           4. Prompts for the restored Viya namespace (default: viya).
#           5. Reads the current cluster/context from the kubeconfig and
#              builds the new FQDN as <namespace>.contour.<cluster>.
#           6. Exports the sas-httpproxy-root HTTPProxy YAML, updates
#              virtualhost.fqdn non-interactively (no `kubectl edit`), and
#              re-applies it with `kubectl apply`.
#           7. Validates the applied HTTPProxy is Valid with the new FQDN.
#
# Input   : environment.properties (auto-loaded from ../environment.properties)
#             KUBECONFIG_PATH — kubeconfig for the restore cluster
#             CLUSTER_TYPE    — must be "restore"
# Optional: NAMESPACE argument ($1) to skip the interactive namespace prompt.
#
# Usage:
#   ./scripts/update_contour_httpproxy.sh
#   ./scripts/update_contour_httpproxy.sh viya
# =============================================================================

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ENV_FILE="${ENV_FILE:-$SCRIPT_DIR/../environment.properties}"
HTTPPROXY_NAME="sas-httpproxy-root"

TMP_DIR=""

# ── Logging helpers ──────────────────────────────────────────────────────────
log_info()    { printf '[INFO]  %s\n' "$1"; }
log_warn()    { printf '[WARN]  %s\n' "$1" >&2; }
log_error()   { printf '[ERROR] %s\n' "$1" >&2; }
log_success() { printf '[OK]    %s\n' "$1"; }

fail() {
	log_error "$1"
	exit 1
}

# ── Cleanup ──────────────────────────────────────────────────────────────────
cleanup() {
	if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
		rm -rf "$TMP_DIR"
	fi
}
trap cleanup EXIT INT TERM

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "Required command not found in PATH: $1"
}

# ── Step 1: Load environment.properties ─────────────────────────────────────
load_environment_properties() {
	[ -f "$ENV_FILE" ] || fail "environment.properties not found at: $ENV_FILE"
	[ -r "$ENV_FILE" ] || fail "environment.properties is not readable: $ENV_FILE"

	KUBECONFIG_PATH=""
	CLUSTER_TYPE=""

	while IFS='=' read -r key value; do
		case "$key" in \#*|"") continue ;; esac
		key=$(printf '%s' "$key" | tr -d '[:space:]')
		value=$(printf '%s' "$value" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
		case "$key" in
			KUBECONFIG_PATH) KUBECONFIG_PATH="$value" ;;
			CLUSTER_TYPE)    CLUSTER_TYPE="$value" ;;
		esac
	done < "$ENV_FILE"

	[ -n "$KUBECONFIG_PATH" ] || fail "KUBECONFIG_PATH is not set in $ENV_FILE"
	[ -n "$CLUSTER_TYPE" ] || fail "CLUSTER_TYPE is not set in $ENV_FILE"

	log_success "Loaded environment.properties from: $ENV_FILE"
}

# ── Step 2: Validate CLUSTER_TYPE=restore ───────────────────────────────────
validate_cluster_type() {
	if [ "$(printf '%s' "$CLUSTER_TYPE" | tr '[:upper:]' '[:lower:]')" != "restore" ]; then
		log_info "This script only applies to the restore cluster."
		log_info "CLUSTER_TYPE is currently set to: $CLUSTER_TYPE"
		log_info "Set CLUSTER_TYPE=restore in $ENV_FILE and re-run."
		exit 0
	fi
	log_success "CLUSTER_TYPE=restore validated."
}

# ── Step 3: Export KUBECONFIG and validate connectivity ─────────────────────
setup_kubeconfig() {
	[ -f "$KUBECONFIG_PATH" ] || fail "KUBECONFIG_PATH file not found: $KUBECONFIG_PATH"
	[ -r "$KUBECONFIG_PATH" ] || fail "KUBECONFIG_PATH file is not readable: $KUBECONFIG_PATH"

	export KUBECONFIG="$KUBECONFIG_PATH"
	log_success "KUBECONFIG exported: $KUBECONFIG"
}

validate_kube_connectivity() {
	require_command kubectl

	if ! kubectl --request-timeout=15s version >/dev/null 2>&1; then
		fail "Invalid kubeconfig or unable to reach the Kubernetes API using: $KUBECONFIG_PATH"
	fi
	if ! kubectl --request-timeout=15s get namespace >/dev/null 2>&1; then
		fail "Cluster connectivity check failed (kubectl get namespace) using: $KUBECONFIG_PATH"
	fi
	log_success "Cluster connectivity validated."
}

# ── Step 4: Prompt for namespace ────────────────────────────────────────────
prompt_namespace() {
	local default_ns="viya"
	local supplied="${1:-}"

	if [ -n "$supplied" ]; then
		NAMESPACE="$supplied"
	elif [ -t 0 ]; then
		printf 'Restored Viya namespace [%s]: ' "$default_ns"
		read -r NAMESPACE
		NAMESPACE="${NAMESPACE:-$default_ns}"
	else
		log_warn "No interactive terminal detected; defaulting namespace to: $default_ns"
		NAMESPACE="$default_ns"
	fi

	[ -n "$NAMESPACE" ] || fail "Namespace must not be empty."

	if ! kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
		fail "Namespace not found on the restore cluster: $NAMESPACE"
	fi
	log_success "Using namespace: $NAMESPACE"
}

# ── Step 5: Read cluster/context and build FQDN ─────────────────────────────
get_cluster_context() {
	CONTEXT_NAME=$(kubectl config current-context 2>/dev/null || true)
	[ -n "$CONTEXT_NAME" ] || fail "Unable to read the current context from kubeconfig: $KUBECONFIG_PATH"

	CLUSTER_NAME=$(kubectl config view --minify -o jsonpath='{.clusters[0].name}' 2>/dev/null || true)
	if [ -z "$CLUSTER_NAME" ]; then
		CLUSTER_NAME="$CONTEXT_NAME"
	fi

	log_success "Cluster/context resolved: $CLUSTER_NAME"
}

build_fqdn() {
	[ -n "$NAMESPACE" ] || fail "Unable to build FQDN: namespace is empty."
	[ -n "$CLUSTER_NAME" ] || fail "Unable to build FQDN: cluster/context is empty."

	NEW_FQDN="${NAMESPACE}.contour.${CLUSTER_NAME}"
	log_info "Computed new FQDN: $NEW_FQDN"

	if [ -t 0 ]; then
		printf 'Is this FQDN correct? [yes/N]: '
		read -r FQDN_CONFIRM
		if [ "${FQDN_CONFIRM}" != "yes" ]; then
			printf 'Enter the correct new FQDN: '
			read -r CUSTOM_FQDN
			CUSTOM_FQDN=$(printf '%s' "${CUSTOM_FQDN:-}" | tr -d '[:space:]')
			[ -n "$CUSTOM_FQDN" ] || fail "FQDN must not be empty."
			NEW_FQDN="$CUSTOM_FQDN"
		fi
	fi

	log_success "New FQDN: $NEW_FQDN"
}

# ── Step 6: Fetch current HTTPProxy state ───────────────────────────────────
fetch_current_httpproxy() {
	if ! kubectl get httpproxy "$HTTPPROXY_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
		fail "HTTPProxy '$HTTPPROXY_NAME' not found in namespace '$NAMESPACE'"
	fi

	CURRENT_FQDN=$(kubectl get httpproxy "$HTTPPROXY_NAME" -n "$NAMESPACE" \
		-o jsonpath='{.spec.virtualhost.fqdn}' 2>/dev/null || true)
	[ -n "$CURRENT_FQDN" ] || fail "Unable to read current virtualhost.fqdn for '$HTTPPROXY_NAME'"

	log_success "Current FQDN: $CURRENT_FQDN"
}

# ── Step 7: Show summary and confirm ────────────────────────────────────────
confirm_changes() {
	echo ""
	echo "=============================================="
	echo " Contour HTTPProxy Update Summary"
	echo "=============================================="
	echo " Cluster type    : $CLUSTER_TYPE"
	echo " KUBECONFIG      : $KUBECONFIG_PATH"
	echo " Namespace       : $NAMESPACE"
	echo " Cluster/context : $CLUSTER_NAME"
	echo " Current FQDN    : $CURRENT_FQDN"
	echo " New FQDN        : $NEW_FQDN"
	echo "=============================================="
	echo ""

	if [ "$CURRENT_FQDN" = "$NEW_FQDN" ]; then
		log_info "Current FQDN already matches the new FQDN. No update necessary."
		exit 0
	fi

	printf 'Proceed with applying this HTTPProxy update? [yes/N]: '
	read -r CONFIRM
	if [ "$CONFIRM" != "yes" ]; then
		log_info "Aborted by user. No changes were applied."
		exit 0
	fi
}

# ── Step 8: Export, update, and apply (no kubectl edit) ─────────────────────
export_update_apply() {
	TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/update-contour-httpproxy.XXXXXX") \
		|| fail "Unable to create a temporary working directory."

	local orig_yaml="$TMP_DIR/httpproxy-original.yaml"
	local updated_yaml="$TMP_DIR/httpproxy-updated.yaml"

	if ! kubectl get httpproxy "$HTTPPROXY_NAME" -n "$NAMESPACE" \
		-o yaml --show-managed-fields=false > "$orig_yaml" 2>/dev/null; then
		fail "Unable to export HTTPProxy YAML for '$HTTPPROXY_NAME'"
	fi
	[ -s "$orig_yaml" ] || fail "Exported HTTPProxy YAML is empty."

	# Drop only status and update fqdn to avoid breaking multi-line YAML annotations.
	if ! awk '/^status:/{exit} {print}' "$orig_yaml" \
		| sed -E "s|^([[:space:]]*fqdn:)[[:space:]].*|\\1 ${NEW_FQDN}|" \
		> "$updated_yaml"; then
		fail "Failed to build the updated HTTPProxy YAML."
	fi
	[ -s "$updated_yaml" ] || fail "Updated HTTPProxy YAML is empty."

	if ! grep -q "fqdn: ${NEW_FQDN}$" "$updated_yaml"; then
		fail "Failed to update virtualhost.fqdn in the exported YAML."
	fi

	if ! kubectl apply --dry-run=client -f "$updated_yaml" >/dev/null 2>&1; then
		fail "Updated HTTPProxy YAML failed client-side validation: $updated_yaml"
	fi

	if ! kubectl apply -n "$NAMESPACE" -f "$updated_yaml" >/dev/null; then
		fail "kubectl apply failed for HTTPProxy '$HTTPPROXY_NAME'"
	fi

	log_success "HTTPProxy '$HTTPPROXY_NAME' applied with updated virtualhost.fqdn."
}

# ── Step 9: Validate after apply ────────────────────────────────────────────
validate_after_apply() {
	if ! kubectl get httpproxy "$HTTPPROXY_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
		fail "Validation failed: HTTPProxy '$HTTPPROXY_NAME' not found after apply."
	fi

	local applied_fqdn
	applied_fqdn=$(kubectl get httpproxy "$HTTPPROXY_NAME" -n "$NAMESPACE" \
		-o jsonpath='{.spec.virtualhost.fqdn}' 2>/dev/null || true)
	[ "$applied_fqdn" = "$NEW_FQDN" ] || \
		fail "Validation failed: virtualhost.fqdn is '$applied_fqdn', expected '$NEW_FQDN'"
	log_success "virtualhost.fqdn matches the new FQDN."

	echo ""
	echo "Current HTTPProxy list:"
	kubectl get httpproxy -n "$NAMESPACE" | grep -E "^NAME|^${HTTPPROXY_NAME}[[:space:]]" || true
	echo ""

	if ! kubectl get httpproxy -n "$NAMESPACE" | grep -F "$NEW_FQDN" >/dev/null 2>&1; then
		fail "Validation failed: 'kubectl get httpproxy' does not show the new FQDN."
	fi
	log_success "'kubectl get httpproxy' shows the new FQDN."

	local timeout_seconds=60
	local interval_seconds=5
	local elapsed=0
	local status_ok="false"

	while [ "$elapsed" -lt "$timeout_seconds" ]; do
		local condition_status
		condition_status=$(kubectl get httpproxy "$HTTPPROXY_NAME" -n "$NAMESPACE" \
			-o jsonpath='{.status.conditions[?(@.type=="Valid")].status}' 2>/dev/null || true)
		local legacy_status
		legacy_status=$(kubectl get httpproxy "$HTTPPROXY_NAME" -n "$NAMESPACE" \
			-o jsonpath='{.status.currentStatus}' 2>/dev/null || true)

		if [ "$condition_status" = "True" ] || \
			[ "$(printf '%s' "$legacy_status" | tr '[:upper:]' '[:lower:]')" = "valid" ]; then
			status_ok="true"
			break
		fi

		sleep "$interval_seconds"
		elapsed=$((elapsed + interval_seconds))
	done

	[ "$status_ok" = "true" ] || \
		fail "Validation failed: HTTPProxy '$HTTPPROXY_NAME' did not reach Valid status within ${timeout_seconds}s."
	log_success "HTTPProxy '$HTTPPROXY_NAME' status is Valid."
}

main() {
	load_environment_properties
	validate_cluster_type
	setup_kubeconfig
	validate_kube_connectivity
	prompt_namespace "${1:-}"
	get_cluster_context
	build_fqdn
	fetch_current_httpproxy
	confirm_changes
	export_update_apply
	validate_after_apply

	echo ""
	log_success "Contour HTTPProxy '$HTTPPROXY_NAME' now routes through: $NEW_FQDN"
}

main "$@"
