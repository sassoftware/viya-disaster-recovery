#!/bin/sh
# Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

set -e

# -----------------------------------------------------------------------------
# update-ingress.sh
#
# Purpose : Run MANUALLY after verifying the Velero restore is healthy.
#           This script is NOT called automatically by --restore.
#
#           Normal run:
#             1. Backs up all current ingress objects to a YAML file
#             2. Detects the DR cluster nginx LoadBalancer address
#             3. Shows the current (source cluster) FQDN from restored ingresses
#             4. Prompts for the new DR cluster FQDN
#             5. Patches every ingress host + TLS host to the new FQDN
#             6. Shows DNS instruction: point new FQDN → DR LB
#
#           Rollback run (--rollback):
#             Reapplies the saved ingress backup YAML to restore original hosts.
#
# Input   : environment.properties (auto-loaded from ../environment.properties)
#             KUBECONFIG_PATH  — kubeconfig for the DR cluster
#             VIYA_NAMESPACE   — Viya namespace
# Optional: NEW_INGRESS_HOST  — pre-set new DR FQDN to skip the prompt (CI use)
#           NAMESPACE          — override VIYA_NAMESPACE if needed
#           BACKUP_DIR         — override backup directory
#
# Usage:
#   ./scripts/update-ingress.sh                                        # interactive
#   NEW_INGRESS_HOST=viya.dr.example.com ./scripts/update-ingress.sh  # non-interactive
#   ./scripts/update-ingress.sh --rollback                             # rollback
# -----------------------------------------------------------------------------

# ── Load environment.properties ───────────────────────────────────────────────
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ENV_FILE="$SCRIPT_DIR/../environment.properties"

if [ ! -f "$ENV_FILE" ]; then
  echo "❌ environment.properties not found at: $ENV_FILE"
  exit 1
fi

while IFS='=' read -r key value; do
  case "$key" in \#*|"") continue ;; esac
  key=$(echo "$key" | tr -d ' ')
  value=$(echo "$value" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
  case "$key" in
    KUBECONFIG_PATH) KUBECONFIG="$value" ;;
    VIYA_NAMESPACE)  NAMESPACE="${NAMESPACE:-$value}" ;;
    BACKUP_DIR)      BACKUP_DIR="${BACKUP_DIR:-$value}" ;;
  esac
done < "$ENV_FILE"

export KUBECONFIG

NAMESPACE="${NAMESPACE:-}"
BACKUP_DIR="${BACKUP_DIR:-$SCRIPT_DIR/../ingress-backup}"
LATEST_BACKUP_FILE="$BACKUP_DIR/ingress-backup-latest.yaml"

if [ -z "$NAMESPACE" ]; then
  echo "❌ NAMESPACE is not set (set VIYA_NAMESPACE in environment.properties)."
  exit 1
fi

# ── Rollback mode ─────────────────────────────────────────────────────────────
if [ "${1:-}" = "--rollback" ]; then
  echo "========================================"
  echo " Ingress Rollback"
  echo "========================================"
  echo "Namespace   : $NAMESPACE"
  echo "Backup file : $LATEST_BACKUP_FILE"
  echo ""
  if [ ! -f "$LATEST_BACKUP_FILE" ]; then
    echo "❌ No ingress backup found at: $LATEST_BACKUP_FILE"
    echo "   Run the normal script first — it saves a backup before patching."
    exit 1
  fi
  printf "   Restore all ingresses from backup? [yes/N]: "
  read -r CONFIRM
  if [ "$CONFIRM" != "yes" ]; then echo "   Rollback cancelled."; exit 0; fi
  echo ""
  echo "   Rolling back ingress hosts from backup..."

  TMPPY=$(mktemp /tmp/rollback_ingress_XXXXXX.py)
  cat > "$TMPPY" << 'PYEOF'
import subprocess, json, sys

backup_file = sys.argv[1]
ns          = sys.argv[2]

# Parse the backup YAML using python
try:
    import yaml
except ImportError:
    # fallback: kubectl apply if yaml not available
    r = subprocess.run(["kubectl", "apply", "-f", backup_file, "-n", ns],
                       capture_output=True, text=True)
    print(r.stdout or r.stderr)
    sys.exit(r.returncode)

with open(backup_file) as f:
    content = f.read()

docs  = list(yaml.safe_load_all(content))
items = []
for doc in docs:
    if not doc:
        continue
    if doc.get("kind") == "List":
        items.extend(doc.get("items", []))
    else:
        items.append(doc)

total   = len(items)
success = 0
failed  = 0
print("   Total ingresses in backup : " + str(total))

for idx, obj in enumerate(items, 1):
    name = obj["metadata"]["name"]
    spec = obj.get("spec", {})
    ops  = []

    for i, rule in enumerate(spec.get("rules", [])):
        host = rule.get("host", "")
        if host:
            ops.append({"op": "replace", "path": "/spec/rules/" + str(i) + "/host", "value": host})

    for ti, tls in enumerate(spec.get("tls", [])):
        for hi, host in enumerate(tls.get("hosts", [])):
            if host:
                ops.append({"op": "replace", "path": "/spec/tls/" + str(ti) + "/hosts/" + str(hi), "value": host})

    if not ops:
        continue

    r = subprocess.run(
        ["kubectl", "patch", "ingress/" + name, "-n", ns, "--type=json", "-p=" + json.dumps(ops)],
        capture_output=True, text=True
    )
    if r.returncode != 0:
        failed += 1
        print("   [" + str(idx) + "/" + str(total) + "] FAIL : " + name + " — " + r.stderr.strip())
    else:
        success += 1
        print("   [" + str(idx) + "/" + str(total) + "] OK   : " + name)

print("")
print("   ✅ Restored : " + str(success))
if failed > 0:
    print("   ❌ Failed  : " + str(failed))
sys.exit(1 if failed > 0 else 0)
PYEOF

  python3 "$TMPPY" "$LATEST_BACKUP_FILE" "$NAMESPACE"
  RC=$?
  rm -f "$TMPPY"

  echo ""
  if [ $RC -eq 0 ]; then
    echo "✅ Rollback complete."
  else
    echo "⚠️  Rollback finished with some failures. Check output above."
  fi
  echo "   ⓘ  Update DNS to re-point the original FQDN back to the source cluster LB."
  echo "========================================"
  exit $RC
fi

# ── Normal run ────────────────────────────────────────────────────────────────
echo "========================================"
echo " Viya Ingress — Post Restore Update"
echo "========================================"
echo "Namespace : $NAMESPACE"
echo ""

# ── Step 1: Back up all current ingress objects ───────────────────────────────
echo "💾 Step 1: Backing up current ingress objects..."

INGRESS_NAMES=$(kubectl get ingress -n "$NAMESPACE" -o name 2>/dev/null || true)
if [ -z "$INGRESS_NAMES" ]; then
  echo "❌ No ingress resources found in namespace: $NAMESPACE"
  echo "   Ensure the Velero restore has completed."
  exit 1
fi

mkdir -p "$BACKUP_DIR"
BACKUP_FILE="$BACKUP_DIR/ingress-backup-$(date +%Y%m%d-%H%M%S).yaml"
kubectl get ingress -n "$NAMESPACE" -o yaml > "$BACKUP_FILE"
cp "$BACKUP_FILE" "$LATEST_BACKUP_FILE"

INGRESS_COUNT=$(echo "$INGRESS_NAMES" | wc -l | tr -d ' ')
echo "   ✅ Backed up $INGRESS_COUNT ingress(es) → $BACKUP_FILE"
echo ""

# ── Step 2: Detect DR cluster nginx LB address ────────────────────────────────
echo "🔍 Step 2: Detecting DR cluster nginx LoadBalancer address..."

LB_HOSTNAME=""
LB_IP=""
LB_HOSTNAME=$(kubectl get svc -n ingress-nginx \
  -o jsonpath='{.items[0].status.loadBalancer.ingress[0].hostname}' 2>/dev/null || true)
if [ -z "$LB_HOSTNAME" ]; then
  LB_IP=$(kubectl get svc -n ingress-nginx \
    -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}' 2>/dev/null || true)
fi
if [ -z "$LB_HOSTNAME" ] && [ -z "$LB_IP" ]; then
  echo "❌ Could not detect nginx ingress LoadBalancer. Check: kubectl get svc -n ingress-nginx"
  exit 1
fi
if [ -n "$LB_HOSTNAME" ]; then
  echo "   ✅ DR LoadBalancer DNS : $LB_HOSTNAME"
else
  echo "   ✅ DR LoadBalancer IP  : $LB_IP"
fi
echo ""

# ── Step 3: Show current (source) FQDN and prompt for new DR FQDN ─────────────
echo "🔍 Step 3: Determining new DR cluster FQDN..."

SOURCE_FQDN=$(kubectl get ingress -n "$NAMESPACE" \
  -o jsonpath='{.items[0].spec.rules[0].host}' 2>/dev/null || true)

if [ -n "$SOURCE_FQDN" ]; then
  echo "   Current (source cluster) FQDN on restored ingresses: $SOURCE_FQDN"
fi
echo ""

if [ -z "$NEW_INGRESS_HOST" ]; then
  printf "   Enter the new DR cluster FQDN for Viya: "
  read -r NEW_INGRESS_HOST
  NEW_INGRESS_HOST=$(echo "$NEW_INGRESS_HOST" | tr -d ' ')
fi

if [ -z "$NEW_INGRESS_HOST" ]; then
  echo "❌ No FQDN provided. Cannot patch ingress rules."
  exit 1
fi

echo "   ✅ New DR FQDN : $NEW_INGRESS_HOST"
echo ""

# ── Step 4: Patch every ingress — replace all host + TLS host fields ──────────
echo "🔧 Step 4: Patching ingress host fields → $NEW_INGRESS_HOST"
echo ""

# Write the python patcher to a temp file (avoids heredoc-in-loop issues in ksh/sh)
TMPPY=$(mktemp /tmp/patch_ingress_XXXXXX.py)
cat > "$TMPPY" << 'PYEOF'
import subprocess, json, sys

ns       = sys.argv[1]
new_fqdn = sys.argv[2]

# Get all ingresses at once
r = subprocess.run(
    ["kubectl", "get", "ingress", "-n", ns, "-o", "json"],
    capture_output=True, text=True
)
if r.returncode != 0:
    print("ERROR fetching ingresses:", r.stderr.strip())
    sys.exit(1)

items = json.loads(r.stdout).get("items", [])
total   = len(items)
success = 0
failed  = 0
skipped = 0

print("   Total ingresses found : " + str(total))

for idx, obj in enumerate(items, 1):
    name = obj["metadata"]["name"]
    spec = obj.get("spec", {})
    ops  = []

    for i, rule in enumerate(spec.get("rules", [])):
        host = rule.get("host", "")
        if not host:
            continue
        new_host = "*." + new_fqdn if host.startswith("*.") else new_fqdn
        ops.append({"op": "replace", "path": "/spec/rules/" + str(i) + "/host", "value": new_host})

    for ti, tls in enumerate(spec.get("tls", [])):
        for hi, host in enumerate(tls.get("hosts", [])):
            if not host:
                continue
            new_host = "*." + new_fqdn if host.startswith("*.") else new_fqdn
            ops.append({"op": "replace", "path": "/spec/tls/" + str(ti) + "/hosts/" + str(hi), "value": new_host})

    if not ops:
        skipped += 1
        print("   [" + str(idx) + "/" + str(total) + "] SKIP (no host fields) : " + name)
        continue

    r2 = subprocess.run(
        ["kubectl", "patch", "ingress/" + name, "-n", ns, "--type=json", "-p=" + json.dumps(ops)],
        capture_output=True, text=True
    )
    if r2.returncode != 0:
        failed += 1
        print("   [" + str(idx) + "/" + str(total) + "] FAIL : " + name + " — " + r2.stderr.strip())
    else:
        success += 1
        print("   [" + str(idx) + "/" + str(total) + "] OK   : " + name)

print("")
print("   ✅ Patched  : " + str(success))
print("   ⏭  Skipped  : " + str(skipped))
if failed > 0:
    print("   ❌ Failed   : " + str(failed))
sys.exit(1 if failed > 0 else 0)
PYEOF

python3 "$TMPPY" "$NAMESPACE" "$NEW_INGRESS_HOST"
RC=$?
rm -f "$TMPPY"

if [ $RC -ne 0 ]; then
  echo "   ⚠️  Some ingresses failed to patch. Check output above."
fi
echo ""

# ── Step 5: Summary ───────────────────────────────────────────────────────────
echo "========================================"
echo " Summary"
echo "========================================"
echo "Namespace    : $NAMESPACE"
echo "New FQDN     : $NEW_INGRESS_HOST"
if [ -n "$LB_HOSTNAME" ]; then
  echo "DR LB DNS    : $LB_HOSTNAME"
  echo ""
  echo "  ACTION: Create/update DNS CNAME:"
  echo "    $NEW_INGRESS_HOST  →  $LB_HOSTNAME"
else
  echo "DR LB IP     : $LB_IP"
  echo ""
  echo "  ACTION: Create/update DNS A record:"
  echo "    $NEW_INGRESS_HOST  →  $LB_IP"
fi
echo ""
echo "  Verify with:"
echo "    kubectl get ingress -n $NAMESPACE"
echo "    curl -k https://$NEW_INGRESS_HOST/SASLogon"
echo ""
  echo "  If NOT working, rollback ingresses with:"
  echo "    ./scripts/update-ingress.sh --rollback"
  echo "  (backup: $LATEST_BACKUP_FILE)"
  echo "========================================"