#!/bin/sh
set -e

NAMESPACE=${NAMESPACE:-viya}
PERM_PATH=/velero/permissions

echo "Starting permission restore in namespace: $NAMESPACE"

# Check if permission backup PVC exists
if ! kubectl get pvc viya-permission-backup -n $NAMESPACE >/dev/null 2>&1; then
    echo "❌ Permission backup PVC not found in namespace: $NAMESPACE"
    exit 1
fi

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
    echo "  Found NFS PVC: $pvc (PV: $PV, Driver: $DRIVER)"
  else
    echo "  Skipping non-NFS PVC: $pvc (Driver: $DRIVER)"
  fi
done

if [ -z "$(echo $NFS_PVCS | tr -d ' ')" ]; then
    echo "No NFS PVCs found to restore in namespace: $NAMESPACE"
    exit 0
fi

echo "NFS PVCs to restore: $NFS_PVCS"

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

echo "Creating dynamic permission restore job with $MOUNT_INDEX NFS PVC mounts..."

cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: viya-permission-restore
  namespace: ${NAMESPACE}
spec:
  backoffLimit: 3
  activeDeadlineSeconds: 3600  # 1 hour timeout for large environments
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: restore
        image: busybox:1.36
        securityContext:
          runAsUser: 0
          capabilities:
            add: ["DAC_OVERRIDE", "FOWNER", "CHOWN"]
        command:
        - sh
        - -c
        - |
          echo "Starting permission restore for namespace: ${NAMESPACE}"
          echo "=========================================="
          
          # Check available permission files
          echo "📁 Available permission files:"
          ls -la /velero/permissions/*.perms 2>/dev/null || {
            echo "❌ No permission files found in /velero/permissions/"
            exit 1
          }
          
          TOTAL_PVCS=0
          SUCCESSFUL_PVCS=0
          FAILED_PVCS=0
          TOTAL_RESTORED=0
          TOTAL_FAILED_ITEMS=0
          
          echo ""
          echo "Processing NFS PVC permission restores:"
          echo "NFS PVCs: ${NFS_PVCS}"
          echo ""
          
          for dir in /pvc/*; do
            if [ -d "\$dir" ]; then
              pvc_name=\$(basename "\$dir")
              TOTAL_PVCS=\$((TOTAL_PVCS + 1))
              
              echo "📁 Processing NFS PVC: \$pvc_name"
              echo "   Target directory: \$dir"
              
              perm_file="/velero/permissions/\${pvc_name}.perms"
              
              if [ ! -f "\$perm_file" ]; then
                echo "   ⚠️  Permission file not found: \$perm_file"
                FAILED_PVCS=\$((FAILED_PVCS + 1))
                continue
              fi
              
              file_size=\$(wc -c < "\$perm_file")
              line_count=\$(wc -l < "\$perm_file")
              echo "   📋 Permission file: \$file_size bytes, \$line_count entries"
              
              restored_count=0
              failed_count=0
              
              echo "   🔄 Restoring permissions..."
              
              while IFS='|' read -r path uid gid mode; do
                # Skip empty lines and malformed entries
                [ -z "\$path" ] && continue
                [ -z "\$uid" ] && continue
                [ -z "\$gid" ] && continue  
                [ -z "\$mode" ] && continue
                
                if [ -e "\$path" ]; then
                  # Restore ownership
                  if chown "\$uid:\$gid" "\$path" 2>/dev/null; then
                    # Restore permissions
                    if chmod "\$mode" "\$path" 2>/dev/null; then
                      restored_count=\$((restored_count + 1))
                      TOTAL_RESTORED=\$((TOTAL_RESTORED + 1))
                      
                      # Progress indicator for large restores
                      if [ \$((restored_count % 5000)) -eq 0 ]; then
                        echo "   Progress: \$restored_count items restored..."
                      fi
                    else
                      failed_count=\$((failed_count + 1))
                      TOTAL_FAILED_ITEMS=\$((TOTAL_FAILED_ITEMS + 1))
                    fi
                  else
                    failed_count=\$((failed_count + 1))
                    TOTAL_FAILED_ITEMS=\$((TOTAL_FAILED_ITEMS + 1))
                  fi
                else
                  # Path doesn't exist - this might be normal for some backup scenarios
                  failed_count=\$((failed_count + 1))
                  TOTAL_FAILED_ITEMS=\$((TOTAL_FAILED_ITEMS + 1))
                fi
              done < "\$perm_file"
              
              if [ \$restored_count -gt 0 ]; then
                success_rate=\$((restored_count * 100 / (restored_count + failed_count + 1)))
                echo "   ✅ PVC Success: \$restored_count restored, \$failed_count failed (\$success_rate% success)"
                SUCCESSFUL_PVCS=\$((SUCCESSFUL_PVCS + 1))
              else
                echo "   ❌ PVC Failed: No items restored"
                FAILED_PVCS=\$((FAILED_PVCS + 1))
              fi
              echo ""
            fi
          done
          
          echo "📊 Restore Summary:"
          echo "=================="
          echo "Total NFS PVCs processed: \$TOTAL_PVCS"
          echo "Successful PVCs: \$SUCCESSFUL_PVCS"
          echo "Failed PVCs: \$FAILED_PVCS"
          echo "Total items restored: \$TOTAL_RESTORED"
          echo "Total failed items: \$TOTAL_FAILED_ITEMS"
          
          if [ \$TOTAL_RESTORED -gt 0 ]; then
            overall_success=\$((TOTAL_RESTORED * 100 / (TOTAL_RESTORED + TOTAL_FAILED_ITEMS + 1)))
            echo "Overall success rate: \$overall_success%"
          fi
          
          if [ \$FAILED_PVCS -gt 0 ] || [ \$TOTAL_FAILED_ITEMS -gt 0 ]; then
            echo "⚠️  Some permission restores failed - check logs above"
            echo "   This may be normal in DR scenarios where file structures differ"
          fi
          
          echo "✅ Permission restore completed!"
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

echo "Waiting for permission restore job to complete..."
kubectl wait --for=condition=complete job/viya-permission-restore -n $NAMESPACE --timeout=3700s

# Check job status and show results
JOB_STATUS=$(kubectl get job viya-permission-restore -n $NAMESPACE -o jsonpath='{.status.conditions[0].type}' 2>/dev/null || echo "Unknown")

if [ "$JOB_STATUS" = "SuccessCriteriaMet" ]; then
    echo "✅ Permission restore job completed successfully"
else
    echo "❌ Permission restore job failed"
    echo "Job status: $JOB_STATUS"
fi

# Cleanup job
kubectl delete job viya-permission-restore -n $NAMESPACE

echo ""
echo "Permission restore complete for namespace: $NAMESPACE"