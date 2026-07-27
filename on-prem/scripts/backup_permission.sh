#!/bin/sh
set -eu

# =============================================================================
# SAS Viya Permission Backup v7
# Captures file ownership and permissions for all PVCs (RWX + RWO)
# NO pod scale-down required — uses kubectl exec for running pods
#
# Changelog:
#   v7: Added 4th fallback (shell-native recursive ls -ldn) for ultra-minimal
#       containers like pgbackrest repo-host (no find, no stat)
#   v6: Alpine jobs install GNU findutils; Crunchy multi-mount fix;
#       3-tier fallback for kubectl exec
#   v5: Full RWO processing loop; RWX capture command; collector job;
#       validation phase
# =============================================================================

NAMESPACE="${1:-viya}"
STORAGE_CLASS="${2:-}"
BACKUP_PVC="viya-permission-backup"

echo "=============================================="
echo "=== SAS Viya Permission Backup v7          ==="
echo "=== Namespace: $NAMESPACE                  ==="
echo "=== NO pod scale-down required!            ==="
echo "=============================================="
echo ""

# --- Cleanup previous runs ---
echo "--- Step 0: Cleaning up previous runs ---"
kubectl delete job -n "$NAMESPACE" -l app=viya-perm-backup 2>/dev/null || true
kubectl delete job viya-permission-snapshot -n "$NAMESPACE" 2>/dev/null || true
kubectl delete job viya-perm-collector -n "$NAMESPACE" 2>/dev/null || true
for old_job in $(kubectl get jobs -n "$NAMESPACE" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}' 2>/dev/null); do
  case "$old_job" in
    viya-rwo-perm-snap-*|viya-csi-perm-snap-*|viya-permission-snapshot|viya-perm-collector|viya-perm-validator|fix-crunchy*|fix-consul*|fix-patroni*|force-promote*)
      echo "  Deleting old job: $old_job"
      kubectl delete job "$old_job" -n "$NAMESPACE" 2>/dev/null || true
      ;;
  esac
done

# Delete and recreate backup PVC fresh
if kubectl get pvc "$BACKUP_PVC" -n "$NAMESPACE" >/dev/null 2>&1; then
  echo "  Deleting old backup PVC: $BACKUP_PVC"
  kubectl delete pvc "$BACKUP_PVC" -n "$NAMESPACE" --wait=true --timeout=120s 2>/dev/null || true
  echo "  Waiting for PVC deletion..."
  sleep 5
fi
echo "Cleanup complete."
echo ""

# --- Detect default StorageClass ---
if [ -z "$STORAGE_CLASS" ]; then
  STORAGE_CLASS=$(kubectl get sc -o jsonpath='{.items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")].metadata.name}')
fi
[ -n "$STORAGE_CLASS" ] || { echo "ERROR: No StorageClass found. Pass as 2nd arg."; exit 1; }
echo "Using StorageClass: $STORAGE_CLASS"
echo ""

# --- Create fresh backup PVC ---
echo "--- Step 1: Creating backup PVC ---"
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${BACKUP_PVC}
  namespace: ${NAMESPACE}
spec:
  accessModes: [ReadWriteMany]
  resources:
    requests:
      storage: 1Gi
  storageClassName: ${STORAGE_CLASS}
EOF
echo "Waiting for backup PVC to be bound..."
kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/"$BACKUP_PVC" -n "$NAMESPACE" --timeout=120s
echo "Backup PVC created and bound."
echo ""

# --- Classify ALL PVCs by access mode ---
echo "--- Step 2: Discovering and classifying PVCs ---"
RWX_PVCS=""
RWO_PVCS=""
CRUNCHY_PVCS=""
CONSUL_PVCS=""

for pvc in $(kubectl get pvc -n "$NAMESPACE" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}'); do
  [ "$pvc" = "$BACKUP_PVC" ] && continue
  pv=$(kubectl get pvc "$pvc" -n "$NAMESPACE" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
  [ -n "$pv" ] || continue

  access_mode=$(kubectl get pvc "$pvc" -n "$NAMESPACE" -o jsonpath='{.spec.accessModes[0]}' 2>/dev/null || true)

  # Tag priority PVCs (Crunchy / Consul)
  case "$pvc" in
    *crunchy*|*postgres*) CRUNCHY_PVCS="$CRUNCHY_PVCS $pvc" ;;
    *consul*)             CONSUL_PVCS="$CONSUL_PVCS $pvc" ;;
  esac

  if [ "$access_mode" = "ReadWriteMany" ]; then
    RWX_PVCS="$RWX_PVCS $pvc"
    echo "  [RWX] $pvc"
  else
    RWO_PVCS="$RWO_PVCS $pvc"
    echo "  [RWO] $pvc"
  fi
done

echo ""
echo "Summary:"
echo "  RWX PVCs (Phase 1): $(echo $RWX_PVCS | wc -w | tr -d ' ') found"
echo "  RWO PVCs (Phase 2): $(echo $RWO_PVCS | wc -w | tr -d ' ') found"
if [ -n "$CRUNCHY_PVCS" ]; then
  echo "  [PRIORITY] Crunchy/Postgres PVCs:$CRUNCHY_PVCS"
fi
if [ -n "$CONSUL_PVCS" ]; then
  echo "  [PRIORITY] Consul PVCs:$CONSUL_PVCS"
fi
echo ""

# ============================================
# PHASE 1: RWX PVCs (single batch job)
# Uses alpine with GNU findutils installed
# ============================================

if [ -n "$RWX_PVCS" ]; then
  echo "=============================================="
  echo "--- Phase 1: Backing up RWX PVC permissions ---"
  echo "=============================================="

  VMOUNTS=""
  VOLS=""
  i=0
  for pvc in $RWX_PVCS; do
    VMOUNTS="${VMOUNTS}
        - name: vol-${i}
          mountPath: /pvc/${pvc}"
    VOLS="${VOLS}
      - name: vol-${i}
        persistentVolumeClaim:
          claimName: ${pvc}"
    i=$((i+1))
  done

  cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: viya-permission-snapshot
  namespace: ${NAMESPACE}
  labels:
    app: viya-perm-backup
spec:
  backoffLimit: 1
  activeDeadlineSeconds: 3600
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: snapshot
        image: alpine:3.20
        securityContext:
          runAsUser: 0
        command: ["/bin/sh","-c"]
        args:
        - |
          echo "Installing GNU findutils (BusyBox find lacks -printf)..."
          apk add --no-cache findutils >/dev/null 2>&1
          mkdir -p /velero/permissions
          echo "=== RWX Permission Snapshot ==="
          total_entries=0
          for dir in /pvc/*/; do
            pvc_name=\$(basename "\$dir")
            out="/velero/permissions/\${pvc_name}.perms"
            echo "Capturing permissions for: \$pvc_name"
            find "\$dir" -maxdepth 5 -printf '%m %U %G %u:%g %p\n' > "\$out" 2>/dev/null || true
            count=\$(wc -l < "\$out" 2>/dev/null || echo 0)
            echo "  Captured \$count entries"
            total_entries=\$((total_entries + count))
          done
          echo "=== RWX Snapshot Complete: \$total_entries total entries ==="
        volumeMounts:
        - name: velero-data
          mountPath: /velero${VMOUNTS}
      volumes:
      - name: velero-data
        persistentVolumeClaim:
          claimName: ${BACKUP_PVC}${VOLS}
EOF

  echo "Waiting for Phase 1 job to complete..."
  kubectl wait --for=condition=complete -n "$NAMESPACE" job/viya-permission-snapshot --timeout=3600s
  echo ""
  echo "Phase 1 job logs:"
  kubectl logs -n "$NAMESPACE" job/viya-permission-snapshot --tail=50
  echo ""
  echo "Phase 1 completed: RWX permissions backed up."
  echo ""
else
  echo "Phase 1 skipped: No RWX PVCs found."
  echo ""
fi

# ============================================
# PHASE 2: RWO PVCs (NO scale-down!)
# 4-tier fallback for kubectl exec:
#   1. GNU find -printf
#   2. stat -c
#   3. ls -ldn (single-level, old fallback)
#   4. Shell-native recursive walk with ls -ldn (NEW in v7)
#      Works in ultra-minimal containers (only sh + ls needed)
# ============================================

if [ -n "$RWO_PVCS" ]; then
  echo "=============================================="
  echo "--- Phase 2: Backing up RWO PVC permissions ---"
  echo "--- (NO scale-down required)               ---"
  echo "=============================================="

  # Create temp dir to collect .perms files locally
  TMPDIR=$(mktemp -d /tmp/viya-perms.XXXXXX)
  echo "Temp dir: $TMPDIR"
  echo ""

  EXEC_OK=""
  UNMOUNTED=""
  FAILED_PVCS=""

  for pvc in $RWO_PVCS; do
    echo "Processing RWO PVC: $pvc"

    # Find a running pod that mounts this PVC
    POD=""
    MOUNT_PATH=""

    for pod in $(kubectl get pods -n "$NAMESPACE" --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}' 2>/dev/null); do
      pvc_match=$(kubectl get pod "$pod" -n "$NAMESPACE" -o jsonpath="{.spec.volumes[?(@.persistentVolumeClaim.claimName==\"$pvc\")].name}" 2>/dev/null || true)
      if [ -n "$pvc_match" ]; then
        vol_name="$pvc_match"
        # v6 FIX: Take only FIRST mount path (Crunchy has 3 containers mounting same PVC)
        mount=$(kubectl get pod "$pod" -n "$NAMESPACE" -o jsonpath="{.spec.containers[*].volumeMounts[?(@.name==\"$vol_name\")].mountPath}" 2>/dev/null | awk '{print $1}')
        if [ -n "$mount" ]; then
          POD="$pod"
          MOUNT_PATH="$mount"
          break
        fi
      fi
    done

    if [ -n "$POD" ] && [ -n "$MOUNT_PATH" ]; then
      echo "  Found running pod: $POD (mount: $MOUNT_PATH)"
      PERMS_FILE="$TMPDIR/${pvc}.perms"

      # -------------------------------------------------------
      # Fallback 1: GNU find -printf (best — captures everything)
      # -------------------------------------------------------
      kubectl exec -n "$NAMESPACE" "$POD" -- \
        find "$MOUNT_PATH" -maxdepth 5 -printf '%m %U %G %u:%g %p\n' \
        > "$PERMS_FILE" 2>/dev/null || true

      # -------------------------------------------------------
      # Fallback 2: stat -c (works in many distros)
      # -------------------------------------------------------
      if [ ! -s "$PERMS_FILE" ]; then
        echo "  GNU find -printf not available, trying stat fallback..."
        kubectl exec -n "$NAMESPACE" "$POD" -- \
          sh -c "find $MOUNT_PATH -maxdepth 5 -exec stat -c '%a %u %g %U:%G %n' {} \;" \
          > "$PERMS_FILE" 2>/dev/null || true
      fi

      # -------------------------------------------------------
      # Fallback 3: find + ls -ldn (find exists but no -printf/stat)
      # -------------------------------------------------------
      if [ ! -s "$PERMS_FILE" ]; then
        echo "  stat fallback failed, trying find + ls -ldn..."
        kubectl exec -n "$NAMESPACE" "$POD" -- \
          sh -c "find $MOUNT_PATH -maxdepth 5 | while read f; do ls -ldn \"\$f\" 2>/dev/null; done" \
          > "$PERMS_FILE" 2>/dev/null || true
        # Mark format if we got data
        if [ -s "$PERMS_FILE" ]; then
          # Prepend format header
          sed -i '1i# FORMAT:LS_LDN' "$PERMS_FILE" 2>/dev/null || true
        fi
      fi

      # -------------------------------------------------------
      # Fallback 4 (NEW v7): Shell-native recursive walk
      # Uses ONLY sh + ls (no find, no stat needed)
      # For ultra-minimal containers like pgbackrest repo-host
      # -------------------------------------------------------
      if [ ! -s "$PERMS_FILE" ]; then
        echo "  All standard tools failed, trying shell-native recursive walk..."
        kubectl exec -n "$NAMESPACE" "$POD" -- \
          sh -c "
            # Shell-native recursive directory walk — no find/stat needed
            rec() {
              for f in \"\$1\"/* \"\$1\"/.*; do
                case \"\$(basename \"\$f\" 2>/dev/null)\" in
                  .|..) continue ;;
                esac
                [ -e \"\$f\" ] || continue
                ls -ldn \"\$f\" 2>/dev/null
                [ -d \"\$f\" ] && rec \"\$f\"
              done
            }
            echo '# FORMAT:LS_LDN'
            ls -ldn $MOUNT_PATH 2>/dev/null
            rec $MOUNT_PATH
          " > "$PERMS_FILE" 2>/dev/null || true
      fi

      if [ -s "$PERMS_FILE" ]; then
        count=$(wc -l < "$PERMS_FILE")
        echo "  Captured $count entries via exec"
        EXEC_OK="$EXEC_OK $pvc"
      else
        echo "  WARNING: All capture methods failed — marking as failed"
        FAILED_PVCS="$FAILED_PVCS $pvc"
      fi
    else
      echo "  No running pod found — will use temp job"
      UNMOUNTED="$UNMOUNTED $pvc"
    fi
  done

  echo ""

  # --- Handle unmounted RWO PVCs with temp jobs ---
  if [ -n "$UNMOUNTED" ]; then
    echo "--- Processing unmounted RWO PVCs via temp jobs ---"
    um_idx=0
    for pvc in $UNMOUNTED; do
      echo "Creating temp job for: $pvc"
      JOB_NAME="viya-rwo-perm-snap-${um_idx}"
      kubectl delete job "$JOB_NAME" -n "$NAMESPACE" 2>/dev/null || true

      cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: ${JOB_NAME}
  namespace: ${NAMESPACE}
  labels:
    app: viya-perm-backup
spec:
  backoffLimit: 1
  activeDeadlineSeconds: 300
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: snapshot
        image: alpine:3.20
        securityContext:
          runAsUser: 0
        command: ["/bin/sh","-c"]
        args:
        - |
          echo "Installing GNU findutils..."
          apk add --no-cache findutils >/dev/null 2>&1
          mkdir -p /velero/permissions
          out="/velero/permissions/${pvc}.perms"
          rm -f "\$out"
          echo "Capturing permissions for unmounted PVC: ${pvc}"
          find /data -maxdepth 5 -printf '%m %U %G %u:%g %p\n' > "\$out" 2>/dev/null || true
          count=\$(wc -l < "\$out" 2>/dev/null || echo 0)
          echo "Captured \$count entries for ${pvc}"
        volumeMounts:
        - name: velero-data
          mountPath: /velero
        - name: target-data
          mountPath: /data
      volumes:
      - name: velero-data
        persistentVolumeClaim:
          claimName: ${BACKUP_PVC}
      - name: target-data
        persistentVolumeClaim:
          claimName: ${pvc}
EOF

      echo "  Waiting for job $JOB_NAME..."
      if kubectl wait --for=condition=complete -n "$NAMESPACE" "job/$JOB_NAME" --timeout=300s 2>/dev/null; then
        echo "  Temp job completed for: $pvc"
        kubectl logs -n "$NAMESPACE" "job/$JOB_NAME" --tail=5 || true
      else
        echo "  WARNING: Temp job failed for: $pvc"
        FAILED_PVCS="$FAILED_PVCS $pvc"
      fi
      um_idx=$((um_idx+1))
    done
    echo ""
  fi

  # --- Copy exec-captured .perms files to backup PVC via collector job ---
  if [ -n "$EXEC_OK" ]; then
    echo "--- Copying exec-captured permissions to backup PVC ---"

    cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: viya-perm-collector
  namespace: ${NAMESPACE}
  labels:
    app: viya-perm-backup
spec:
  backoffLimit: 1
  activeDeadlineSeconds: 300
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: collector
        image: alpine:3.20
        securityContext:
          runAsUser: 0
        command: ["sleep","3600"]
        volumeMounts:
        - name: velero-data
          mountPath: /velero
      volumes:
      - name: velero-data
        persistentVolumeClaim:
          claimName: ${BACKUP_PVC}
EOF

    # Wait for collector pod to be ready
    echo "  Waiting for collector pod..."
    kubectl wait --for=condition=ready -n "$NAMESPACE" pod -l job-name=viya-perm-collector --timeout=120s

    COLLECTOR_POD=$(kubectl get pod -n "$NAMESPACE" -l job-name=viya-perm-collector -o jsonpath='{.items[0].metadata.name}')
    echo "  Collector pod: $COLLECTOR_POD"

    # Ensure target directory exists
    kubectl exec -n "$NAMESPACE" "$COLLECTOR_POD" -- mkdir -p /velero/permissions

    # Copy each .perms file
    for pvc in $EXEC_OK; do
      if [ -f "$TMPDIR/${pvc}.perms" ]; then
        kubectl cp "$TMPDIR/${pvc}.perms" "$NAMESPACE/$COLLECTOR_POD:/velero/permissions/${pvc}.perms"
        echo "  Copied: ${pvc}.perms"
      fi
    done

    # Terminate collector
    kubectl delete job viya-perm-collector -n "$NAMESPACE" --wait=false 2>/dev/null || true
    echo "  Collector job cleaned up."
    echo ""
  fi

  # Cleanup temp dir
  rm -rf "$TMPDIR"

  echo ""
  echo "Phase 2 completed: RWO permissions backed up"
  echo "  Captured via exec:      $(echo $EXEC_OK | wc -w | tr -d ' ') PVCs"
  echo "  Captured via temp job:  $(echo $UNMOUNTED | wc -w | tr -d ' ') PVCs"
  if [ -n "$FAILED_PVCS" ]; then
    echo "  WARNING - Failed PVCs:$FAILED_PVCS"
    echo "  (These will need manual permission fixes during restore)"
  fi
  echo ""
else
  echo "Phase 2 skipped: No RWO PVCs found."
  echo ""
fi

# ============================================
# PHASE 3: Validation
# ============================================

echo "=============================================="
echo "--- Phase 3: Validating permission backups ---"
echo "=============================================="

cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: viya-perm-validator
  namespace: ${NAMESPACE}
  labels:
    app: viya-perm-backup
spec:
  backoffLimit: 1
  activeDeadlineSeconds: 120
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: validator
        image: alpine:3.20
        securityContext:
          runAsUser: 0
        command: ["/bin/sh","-c"]
        args:
        - |
          echo "============================================="
          echo "Permission Backup Validation Report"
          echo "============================================="
          total_files=0
          total_entries=0
          passed_files=""
          failed_files=""
          if [ -d /velero/permissions ]; then
            for f in /velero/permissions/*.perms; do
              [ -f "\$f" ] || continue
              pvc_name=\$(basename "\$f" .perms)
              # Exclude comment/header lines from count
              entries=\$(grep -cv '^#' "\$f" 2>/dev/null || echo 0)
              total_files=\$((total_files + 1))
              total_entries=\$((total_entries + entries))
              # Detect format
              fmt="STANDARD"
              head -1 "\$f" | grep -q '^# FORMAT:LS_LDN' && fmt="LS_LDN"
              if [ "\$entries" -gt 0 ]; then
                echo "  [PASS] \$pvc_name: \$entries entries (format: \$fmt)"
                passed_files="\$passed_files \$pvc_name"
              else
                echo "  [FAIL] \$pvc_name: EMPTY"
                failed_files="\$failed_files \$pvc_name"
              fi
            done
          fi
          echo "============================================="
          echo "Total .perms files: \$total_files"
          echo "Total entries:      \$total_entries"
          if [ -n "\$failed_files" ]; then
            echo "FAILED (empty):    \$failed_files"
            echo "STATUS: PARTIAL SUCCESS"
          elif [ "\$total_files" -eq 0 ]; then
            echo "STATUS: NO FILES FOUND — BACKUP MAY HAVE FAILED"
          else
            echo "STATUS: ALL PASSED"
          fi
          echo "============================================="
        volumeMounts:
        - name: velero-data
          mountPath: /velero
      volumes:
      - name: velero-data
        persistentVolumeClaim:
          claimName: ${BACKUP_PVC}
EOF

echo "Running validation..."
kubectl wait --for=condition=complete -n "$NAMESPACE" job/viya-perm-validator --timeout=120s
echo ""
echo "=== Validation Report ==="
kubectl logs -n "$NAMESPACE" job/viya-perm-validator
echo ""

# Cleanup validator job
kubectl delete job viya-perm-validator -n "$NAMESPACE" 2>/dev/null || true

echo "=============================================="
echo "=== SAS Viya Permission Backup v7 COMPLETE ==="
echo "=== Backup PVC: $BACKUP_PVC               ==="
echo "=== Namespace:  $NAMESPACE                 ==="
echo "=============================================="
