# Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

#!/bin/sh
# Exit immediately if any command returns a non-zero status
set -e

# -----------------------------------------------------------------------------
# update-ingress.sh
#
# Purpose : After a Velero restore, ingress resources in the Viya namespace
#           still reference the source cluster's old hostnames. This script:
#             1. Auto-detects the DR cluster's nginx LoadBalancer hostname/IP
#             2. Reads every ingress in the restored Viya namespace
#             3. Replaces each rule host and TLS host with the new DR hostname
#                (wildcard hosts — those starting with '*.' — keep their prefix)
#
# Input   : NAMESPACE  — Viya namespace (passed from Go automation or manually)
# Auto    : NEW_INGRESS_HOST — detected from 'kubectl get svc -n ingress-nginx'
#
# Manual usage:
#   NAMESPACE=viya4 ./scripts/update-ingress.sh
# -----------------------------------------------------------------------------

# Validate required input variable
if [ -z "$NAMESPACE" ]; then
  echo "❌ NAMESPACE is not set. Pass NAMESPACE=<viya-namespace> when running manually."
  exit 1
fi

echo "========================================"
echo "Viya Ingress Update — Post Restore"
echo "========================================"
echo "Namespace: $NAMESPACE"
echo ""

# ── Step 1: Auto-detect the DR cluster's nginx ingress LoadBalancer hostname/IP ──
echo "🔍 Detecting DR cluster nginx ingress LoadBalancer address..."

# Initialise to empty; populated by kubectl queries below
NEW_INGRESS_HOST=""

# AKS cloud load balancers typically expose a DNS hostname rather than a bare IP
NEW_INGRESS_HOST=$(kubectl get svc -n ingress-nginx \
  -o jsonpath='{.items[0].status.loadBalancer.ingress[0].hostname}' 2>/dev/null || true)

# Some environments expose only an IP; fall back if hostname field is empty
if [ -z "$NEW_INGRESS_HOST" ]; then
  NEW_INGRESS_HOST=$(kubectl get svc -n ingress-nginx \
    -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}' 2>/dev/null || true)
fi

# Abort if neither hostname nor IP could be resolved — ingress controller may not be ready
if [ -z "$NEW_INGRESS_HOST" ]; then
  echo "❌ Could not auto-detect the nginx ingress LoadBalancer address."
  echo "   Ensure the ingress-nginx service has an external IP/hostname assigned:"
  echo "     kubectl get svc -n ingress-nginx"
  exit 1
fi

echo "✅ DR cluster ingress address: $NEW_INGRESS_HOST"
echo ""

# ── Step 2: List all ingress resources in the Viya namespace ──
echo "🔍 Listing ingress resources in namespace: $NAMESPACE"

# Retrieve all ingress object names (e.g. ingress.networking.k8s.io/sas-arke)
INGRESS_NAMES=$(kubectl get ingress -n "$NAMESPACE" -o name 2>/dev/null || true)

# Nothing to do if the restore has not yet created any ingress objects
if [ -z "$INGRESS_NAMES" ]; then
  echo "⚠️  No ingress resources found in namespace: $NAMESPACE"
  echo "   Nothing to patch. Ensure the Velero restore has completed."
  exit 0
fi

# Count total ingresses for progress reporting
INGRESS_COUNT=$(echo "$INGRESS_NAMES" | wc -l | tr -d ' ')
echo "Found $INGRESS_COUNT ingress resource(s):"
for name in $INGRESS_NAMES; do
  echo "  - $name"
done
echo ""

# ── Step 3: Patch each ingress ──
SUCCESS_COUNT=0  # number of ingresses successfully patched
FAIL_COUNT=0     # number of ingresses that failed to patch

for ingress_name in $INGRESS_NAMES; do
  # Strip the resource type prefix to get a readable short name for display
  SHORT_NAME=$(echo "$ingress_name" | sed 's|ingress.networking.k8s.io/||')
  echo "──────────────────────────────────────────"
  echo "Patching: $SHORT_NAME"

  # Display current (old) hosts so the operator can see what is being replaced
  echo "  Current hosts:"
  kubectl get "$ingress_name" -n "$NAMESPACE" \
    -o jsonpath='{range .spec.rules[*]}    rule : {.host}{"\n"}{end}' 2>/dev/null || true
  kubectl get "$ingress_name" -n "$NAMESPACE" \
    -o jsonpath='{range .spec.tls[0].hosts[*]}    tls  : {.}{"\n"}{end}' 2>/dev/null || true
  echo ""

  # Count how many rules this ingress has (one line per host in the rules array)
  RULE_COUNT=$(kubectl get "$ingress_name" -n "$NAMESPACE" \
    -o jsonpath='{range .spec.rules[*]}{.host}{"\n"}{end}' 2>/dev/null | grep -c '.' || echo 0)

  # Start building the JSON patch array for kubectl patch --type=json
  PATCH='['
  NEED_COMMA=0  # tracks whether a comma separator is needed before the next patch op
  i=0

  # Iterate over every rule entry and add a replace operation for its host field
  while [ "$i" -lt "$RULE_COUNT" ]; do
    # Read the current host value at index i
    OLD_HOST=$(kubectl get "$ingress_name" -n "$NAMESPACE" \
      -o jsonpath="{.spec.rules[$i].host}" 2>/dev/null || true)

    if [ -n "$OLD_HOST" ]; then
      # Keep the '*.' wildcard prefix if the original rule was a wildcard entry
      if echo "$OLD_HOST" | grep -q '^\*\.'; then
        NEW_HOST="*.$NEW_INGRESS_HOST"
      else
        NEW_HOST="$NEW_INGRESS_HOST"
      fi
      # Append comma separator before every patch op except the first
      if [ "$NEED_COMMA" -eq 1 ]; then PATCH="$PATCH,"; fi
      PATCH="$PATCH{\"op\":\"replace\",\"path\":\"/spec/rules/$i/host\",\"value\":\"$NEW_HOST\"}"
      NEED_COMMA=1
    fi
    i=$((i + 1))
  done

  # Count how many TLS host entries exist in tls[0].hosts[]
  TLS_COUNT=$(kubectl get "$ingress_name" -n "$NAMESPACE" \
    -o jsonpath='{range .spec.tls[0].hosts[*]}{.}{"\n"}{end}' 2>/dev/null | grep -c '.' || echo 0)

  j=0

  # Iterate over every TLS host entry and add a replace operation
  while [ "$j" -lt "$TLS_COUNT" ]; do
    # Read the current TLS host value at index j
    OLD_TLS=$(kubectl get "$ingress_name" -n "$NAMESPACE" \
      -o jsonpath="{.spec.tls[0].hosts[$j]}" 2>/dev/null || true)

    if [ -n "$OLD_TLS" ]; then
      # Preserve wildcard prefix for TLS entries the same way as rules
      if echo "$OLD_TLS" | grep -q '^\*\.'; then
        NEW_TLS="*.$NEW_INGRESS_HOST"
      else
        NEW_TLS="$NEW_INGRESS_HOST"
      fi
      if [ "$NEED_COMMA" -eq 1 ]; then PATCH="$PATCH,"; fi
      PATCH="$PATCH{\"op\":\"replace\",\"path\":\"/spec/tls/0/hosts/$j\",\"value\":\"$NEW_TLS\"}"
      NEED_COMMA=1
    fi
    j=$((j + 1))
  done

  # Close the JSON patch array
  PATCH="$PATCH]"

  # Apply the patch; on success show the updated hosts so the operator can verify
  if kubectl patch "$ingress_name" -n "$NAMESPACE" --type='json' -p="$PATCH" 2>/dev/null; then
    echo "  New hosts:"
    kubectl get "$ingress_name" -n "$NAMESPACE" \
      -o jsonpath='{range .spec.rules[*]}    rule : {.host}{"\n"}{end}' 2>/dev/null || true
    kubectl get "$ingress_name" -n "$NAMESPACE" \
      -o jsonpath='{range .spec.tls[0].hosts[*]}    tls  : {.}{"\n"}{end}' 2>/dev/null || true
    echo "  ✅ Patched successfully"
    SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
  else
    echo "  ❌ Patch failed for $SHORT_NAME"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  echo ""
done

# ── Summary ──
echo "========================================"
echo "Ingress Update Summary"
echo "========================================"
echo "Namespace  : $NAMESPACE"
echo "New host   : $NEW_INGRESS_HOST"
echo "Patched    : $SUCCESS_COUNT / $INGRESS_COUNT"
# Only print the failure line when there are actual failures to keep output clean
if [ "$FAIL_COUNT" -gt 0 ]; then
  echo "Failed     : $FAIL_COUNT"
fi
echo ""

if [ "$SUCCESS_COUNT" -gt 0 ]; then
  echo "✅ Ingress update complete. Verify with:"
  echo "   kubectl get ingress -n $NAMESPACE"
  echo "   kubectl describe ingress -n $NAMESPACE"
else
  echo "⚠️  No ingress resources were patched."
  echo "   Check the namespace and nginx ingress service status."
fi
echo "========================================"
