# Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

#!/bin/sh
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

# Create the permission backup PVC dynamically with the correct namespace
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

echo "Created permission backup PVC in namespace: $NAMESPACE"

# Get all PVCs and filter for NFS ones
ALL_PVCS=$(kubectl get pvc -n $NAMESPACE -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}')
NFS_PVCS=""

echo "Discovering NFS PVCs..."
for pvc in $ALL_PVCS; do
  # Skip the permission backup PVC itself
  [ "$pvc" = "viya-permission-backup" ] && continue
  
  PV=$(kubectl get pvc $pvc -n $NAMESPACE -o jsonpath='{.spec.volumeName}' 2>/dev/null)
  [ -z "$PV" ] && continue

  DRIVER=$(kubectl get pv $PV -o jsonpath='{.spec.csi.driver}' 2>/dev/null)
  if echo "$DRIVER" | grep -qi nfs; then
    NFS_PVCS="$NFS_PVCS $pvc"
    echo "  Found NFS PVC: $pvc"
  else
    echo "  Skipping non-NFS PVC: $pvc"
  fi
done

if [ -z "$(echo $NFS_PVCS | tr -d ' ')" ]; then
    echo "No NFS PVCs found to backup in namespace: $NAMESPACE"
    exit 0
fi

# Build dynamic volume mounts and volumes for NFS PVCs only
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
          
          echo "Processing NFS PVC data directories:"
          echo "NFS PVCs: ${NFS_PVCS}"
          echo ""
          
          for dir in /pvc/*; do
            if [ -d "\$dir" ]; then
              pvc_name=\$(basename "\$dir")
              TOTAL_PVCS=\$((TOTAL_PVCS + 1))
              
              echo "📁 Processing NFS PVC: \$pvc_name"
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
          echo "Total NFS PVCs processed: \$TOTAL_PVCS"
          echo "Successful: \$SUCCESSFUL_PVCS"  
          echo "Failed: \$FAILED_PVCS"
          echo ""
          echo "📁 Generated permission files:"
          ls -la /velero/permissions/*.perms 2>/dev/null || echo "No permission files found"
          
          if [ \$FAILED_PVCS -gt 0 ]; then
            echo "⚠️  Some NFS PVCs failed to backup - check logs above"
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
kubectl wait --for=condition=complete job/viya-permission-snapshot -n $NAMESPACE --timeout=3700s

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
