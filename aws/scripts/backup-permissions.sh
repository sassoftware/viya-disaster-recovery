#!/bin/sh
# Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
set -e

NAMESPACE=${NAMESPACE:-viya}
PERM_PATH=/velero/permissions

# STORAGE_CLASS is passed from the Go automation tool, which reads it from environment.properties (NFS_STORAGE_CLASS).
# If running this script manually, set it via: STORAGE_CLASS=<your-class> ./scripts/backup-permissions.sh
if [ -z "$STORAGE_CLASS" ]; then
  echo "❌ STORAGE_CLASS is not set. Set NFS_STORAGE_CLASS in environment.properties or pass STORAGE_CLASS=<value> when running manually."
  exit 1
fi

echo "Starting permission backup in namespace: $NAMESPACE"

# Delete any leftover job from a previous run (jobs are immutable — must recreate)
kubectl delete job viya-permission-snapshot -n $NAMESPACE --ignore-not-found

# Delete existing PVC if it exists — access mode and storageClass are immutable once created,
# so we must delete and recreate to ensure the correct spec (ReadWriteMany for EFS CSI)
if kubectl get pvc viya-permission-backup -n $NAMESPACE >/dev/null 2>&1; then
  echo "Deleting existing viya-permission-backup PVC to recreate with correct spec..."
  kubectl delete pvc viya-permission-backup -n $NAMESPACE
  echo "Waiting for PVC deletion to complete..."
  kubectl wait --for=delete pvc/viya-permission-backup -n $NAMESPACE --timeout=60s 2>/dev/null || true
fi

# Create the permission backup PVC using STORAGE_CLASS from environment.properties (NFS_STORAGE_CLASS)
# Same pattern as Azure: one storage class variable controls both PVC detection and PVC creation
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: viya-permission-backup
  namespace: ${NAMESPACE}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: ${STORAGE_CLASS}
EOF

echo "Waiting for permission backup PVC to be bound..."
kubectl wait --for=jsonpath='.status.phase'=Bound pvc/viya-permission-backup -n $NAMESPACE --timeout=120s || {
  echo "❌ PVC viya-permission-backup did not become Bound within 120s"
  echo "   PVC status:"
  kubectl get pvc viya-permission-backup -n $NAMESPACE
  echo "   Check EFS CSI driver: kubectl get pods -n kube-system -l app=efs-csi-node"
  exit 1
}

echo "Created permission backup PVC in namespace: $NAMESPACE"

# Get all PVCs and filter for NFS ones (Amazon EFS CSI driver: efs.csi.aws.com)
ALL_PVCS=$(kubectl get pvc -n $NAMESPACE -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}')
NFS_PVCS=""

echo "Discovering EFS/NFS PVCs..."
for pvc in $ALL_PVCS; do
  # Skip the permission backup PVC itself
  [ "$pvc" = "viya-permission-backup" ] && continue

  PV=$(kubectl get pvc $pvc -n $NAMESPACE -o jsonpath='{.spec.volumeName}' 2>/dev/null)
  [ -z "$PV" ] && continue

  DRIVER=$(kubectl get pv $PV -o jsonpath='{.spec.csi.driver}' 2>/dev/null)
  # Match Amazon EFS CSI driver or any driver containing 'nfs'/'efs'
  if echo "$DRIVER" | grep -qiE 'efs\.csi\.aws\.com|nfs'; then
    NFS_PVCS="$NFS_PVCS $pvc"
    echo "  Found EFS/NFS PVC: $pvc (PV: $PV, Driver: $DRIVER)"
  else
    echo "  Skipping non-EFS/NFS PVC: $pvc (Driver: $DRIVER)"
  fi
done

if [ -z "$(echo $NFS_PVCS | tr -d ' ')" ]; then
    echo "No EFS/NFS PVCs found to backup in namespace: $NAMESPACE"
    exit 0
fi

# Build dynamic volume mounts and volumes for EFS/NFS PVCs only
VOLUME_MOUNTS=""
VOLUMES=""
MOUNT_INDEX=0

for pvc in $NFS_PVCS; do
    # Skip empty entries
    [ -z "$pvc" ] && continue

    # Create volume mount entry
    VOLUME_MOUNTS="$VOLUME_MOUNTS
        - name: vol-$MOUNT_INDEX
          mountPath: /pvc/$pvc"

    # Create volume entry
    VOLUMES="$VOLUMES
      - name: vol-$MOUNT_INDEX
        persistentVolumeClaim:
          claimName: $pvc"

    MOUNT_INDEX=$((MOUNT_INDEX + 1))
done

cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: viya-permission-snapshot
  namespace: ${NAMESPACE}
spec:
  backoffLimit: 3
  activeDeadlineSeconds: 3600  # 1 hour timeout for large environments
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: snapshot
        image: busybox:1.36
        securityContext:
          runAsUser: 0
          capabilities:
            add: ["DAC_OVERRIDE", "FOWNER"]
        command:
        - sh
        - -c
        - |
          echo "Starting permission snapshot for namespace: ${NAMESPACE}"
          echo "=========================================="

          mkdir -p /velero/permissions

          # Clean up any existing permission files
          rm -f /velero/permissions/*.perms

          TOTAL_PVCS=0
          SUCCESSFUL_PVCS=0
          FAILED_PVCS=0

          echo "Processing EFS/NFS PVC data directories:"
          echo "NFS PVCs: ${NFS_PVCS}"
          echo ""

          for dir in /pvc/*; do
            if [ -d "\$dir" ]; then
              pvc_name=\$(basename "\$dir")
              TOTAL_PVCS=\$((TOTAL_PVCS + 1))

              echo "📁 Processing EFS/NFS PVC: \$pvc_name"
              echo "   Source directory: \$dir"

              perm_file="/velero/permissions/\${pvc_name}.perms"

              # Count total items first
              total_items=\$(find "\$dir" -type f -o -type d 2>/dev/null | wc -l)
              echo "   Total items to process: \$total_items"

              if [ "\$total_items" -eq 0 ]; then
                echo "   ⚠️  Directory is empty, creating minimal entry"
                echo "\$dir|0|0|755" > "\$perm_file"
              else
                echo "   🔍 Scanning permissions..."
                processed=0

                find "\$dir" -type f -o -type d 2>/dev/null | while read -r path; do
                  if [ -e "\$path" ]; then
                    uid=\$(stat -c "%u" "\$path" 2>/dev/null || echo "0")
                    gid=\$(stat -c "%g" "\$path" 2>/dev/null || echo "0")
                    mode=\$(stat -c "%a" "\$path" 2>/dev/null || echo "755")
                    echo "\$path|\$uid|\$gid|\$mode" >> "\$perm_file"

                    processed=\$((processed + 1))
                    if [ \$((processed % 5000)) -eq 0 ]; then
                      echo "   Progress: \$processed/\$total_items files processed..."
                    fi
                  fi
                done
              fi

              if [ -f "\$perm_file" ]; then
                file_count=\$(wc -l < "\$perm_file")
                file_size=\$(wc -c < "\$perm_file")
                echo "   ✅ Success: \$file_count entries, \$file_size bytes"
                SUCCESSFUL_PVCS=\$((SUCCESSFUL_PVCS + 1))
              else
                echo "   ❌ Failed to create permission file"
                FAILED_PVCS=\$((FAILED_PVCS + 1))
              fi
              echo ""
            fi
          done

          echo "📊 Backup Summary:"
          echo "=================="
          echo "Total EFS/NFS PVCs processed: \$TOTAL_PVCS"
          echo "Successful: \$SUCCESSFUL_PVCS"
          echo "Failed: \$FAILED_PVCS"
          echo ""
          echo "📁 Generated permission files:"
          ls -la /velero/permissions/*.perms 2>/dev/null || echo "No permission files found"

          if [ \$FAILED_PVCS -gt 0 ]; then
            echo "⚠️  Some EFS/NFS PVCs failed to backup - check logs above"
          fi

          echo "✅ Permission snapshot completed successfully!"
        resources:
          requests:
            memory: "128Mi"
            cpu: "200m"
          limits:
            memory: "512Mi"
            cpu: "1000m"
        volumeMounts:${VOLUME_MOUNTS}
        - name: velero-data
          mountPath: /velero
      volumes:${VOLUMES}
      - name: velero-data
        persistentVolumeClaim:
          claimName: viya-permission-backup
EOF

echo "Waiting for permission snapshot job to complete..."
kubectl wait --for=condition=Complete job/viya-permission-snapshot -n $NAMESPACE --timeout=3700s || {
  echo "❌ Permission snapshot job did not complete within timeout"
  echo "   Job status:"
  kubectl get job viya-permission-snapshot -n $NAMESPACE
  echo "   Pod status:"
  kubectl get pods -n $NAMESPACE -l job-name=viya-permission-snapshot
  echo "   Pod logs:"
  kubectl logs -n $NAMESPACE -l job-name=viya-permission-snapshot --tail=50 2>/dev/null || true
  exit 1
}

# Check job status and show results
JOB_STATUS=$(kubectl get job viya-permission-snapshot -n $NAMESPACE -o jsonpath='{.status.conditions[0].type}' 2>/dev/null || echo "Unknown")

if [ "$JOB_STATUS" = "SuccessCriteriaMet" ]; then
    echo "✅ Permission snapshot job completed successfully"
else
    echo "❌ Permission snapshot job failed"
    echo "Job status: $JOB_STATUS"
fi

# Cleanup job
kubectl delete job viya-permission-snapshot -n $NAMESPACE

echo ""
echo "Permission backup complete for namespace: $NAMESPACE"
