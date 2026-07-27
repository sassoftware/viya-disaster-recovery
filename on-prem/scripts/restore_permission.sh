#!/bin/bash
set -euo pipefail

# =============================================================================
# SAS Viya Permission Restore v12
# - Restores BOTH RWX and RWO PVC permissions/ownership after Velero restore
# - Fixes RWO path rebasing: /pgdata -> /target, /consul/data -> /target, etc.
# - Adds RWX path rebasing: /pvc/<pvc-name> -> /target
# - Includes CAS/RWX hardening for CAS + QKB PVCs
# - Cleans permission restore jobs
# - Optionally restarts restored stateful/CAS pods and cleans stale workload-orchestrator services
# =============================================================================

NAMESPACE="${1:-viya}"
BACKUP_PVC="${BACKUP_PVC:-viya-permission-backup}"
JOB_PREFIX="viya-perm-restore-v12"
RESTART_AFTER="${RESTART_AFTER:-true}"
CLEANUP_JOBS_AFTER="${CLEANUP_JOBS_AFTER:-true}"
CLEANUP_STALE_WO_SERVICES="${CLEANUP_STALE_WO_SERVICES:-true}"
FORCE_CAS_OWNERSHIP="${FORCE_CAS_OWNERSHIP:-true}"

echo "=============================================="
echo " SAS Viya Permission Restore v12"
echo " Namespace:                 ${NAMESPACE}"
echo " Backup PVC:                ${BACKUP_PVC}"
echo " Restart after restore:     ${RESTART_AFTER}"
echo " Cleanup jobs after:        ${CLEANUP_JOBS_AFTER}"
echo " Cleanup stale WO services: ${CLEANUP_STALE_WO_SERVICES}"
echo "=============================================="
echo ""

if ! kubectl get pvc "${BACKUP_PVC}" -n "${NAMESPACE}" >/dev/null 2>&1; then
  echo "ERROR: Backup PVC '${BACKUP_PVC}' not found in namespace '${NAMESPACE}'"
  exit 1
fi

kubectl delete job -n "${NAMESPACE}" -l app=viya-perm-restore-v12 >/dev/null 2>&1 || true
kubectl delete job -n "${NAMESPACE}" -l app=viya-perm-restore-v11 >/dev/null 2>&1 || true

TARGET_PVCS=""
for pvc in $(kubectl get pvc -n "${NAMESPACE}" -o name | sed 's#persistentvolumeclaim/##' | sort); do
  [ "${pvc}" = "${BACKUP_PVC}" ] && continue
  TARGET_PVCS="${TARGET_PVCS} ${pvc}"
done

if [ -z "${TARGET_PVCS// }" ]; then
  echo "No PVCs found in namespace ${NAMESPACE}."
  exit 0
fi

echo "PVCs to process:${TARGET_PVCS}"
echo ""

ok=0
skip=0
fail=0
idx=0

for pvc in ${TARGET_PVCS}; do
  access_mode=$(kubectl get pvc "${pvc}" -n "${NAMESPACE}" -o jsonpath='{.spec.accessModes[0]}' 2>/dev/null || echo "Unknown")
  job="${JOB_PREFIX}-${idx}"
  idx=$((idx+1))

  echo "=============================================="
  echo "Restoring PVC: ${pvc} (${access_mode})"
  echo "=============================================="

  kubectl delete job "${job}" -n "${NAMESPACE}" >/dev/null 2>&1 || true

  job_file=$(mktemp /tmp/viya-perm-restore-v12.XXXXXX.yaml)
  cat > "${job_file}" <<'YAML'
apiVersion: batch/v1
kind: Job
metadata:
  name: __JOB_NAME__
  namespace: __NAMESPACE__
  labels:
    app: viya-perm-restore-v12
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 1800
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: restore
        image: alpine:3.20
        securityContext:
          runAsUser: 0
        command: ["/bin/sh", "-c"]
        args:
        - |
          set -eu
          PVC="__PVC__"
          ACCESS_MODE="__ACCESS_MODE__"
          FORCE_CAS_OWNERSHIP="__FORCE_CAS_OWNERSHIP__"
          PERMS_FILE="/backup/permissions/__PVC__.perms"
          TARGET_ROOT="/target"

          echo "PVC: ${PVC}"
          echo "Access mode: ${ACCESS_MODE}"
          echo "Perms file: ${PERMS_FILE}"

          if [ ! -f "${PERMS_FILE}" ]; then
            echo "[SKIP] No permission file found for ${PVC}"
            exit 10
          fi

          get_first_path() {
            grep -v '^#' "${PERMS_FILE}" | grep -v '^total' | awk '
              NF>=5 {for(i=5;i<=NF;i++){printf "%s", $i; if(i<NF) printf " "}; print ""; exit}
              NF==4 {print $4; exit}
            ' || true
          }

          FIRST_PATH="$(get_first_path)"
          [ -n "${FIRST_PATH}" ] || FIRST_PATH="/data"

          # Source mount root detection.
          # RWX permission backup jobs normally captured paths as /pvc/<pvc-name>/...
          # RWO backups captured original container mount roots such as /pgdata, /consul/data, /rabbitmq/data.
          case "${PVC}" in
            cas-default-data|cas-default-permstore|sas-cas-backup-data|sas-common-backup-data|sas-commonfiles|sas-pyconfig|sas-quality-knowledge-base|gelldap-*|*commonfiles*|*cas-*|*quality*)
              if echo "${FIRST_PATH}" | grep -q "^/pvc/${PVC}\($\|/\)"; then
                SRC_ROOT="/pvc/${PVC}"
              else
                SRC_ROOT="${FIRST_PATH}"
              fi
              ;;
            data-sas-opendistro-*|*opendistro*) SRC_ROOT="/usr/share/elasticsearch/data" ;;
            sas-crunchy-platform-postgres-*-pgdata|*pgdata) SRC_ROOT="/pgdata" ;;
            sas-crunchy-platform-postgres-repo1|*repo1) SRC_ROOT="/pgbackrest/repo1" ;;
            sas-viya-consul-data-volume-*|*consul*) SRC_ROOT="/consul/data" ;;
            sas-viya-rabbitmq-data-volume-*|*rabbitmq*) SRC_ROOT="/rabbitmq/data" ;;
            sas-viya-redis-data-volume-*|*redis*) SRC_ROOT="/data" ;;
            *)
              if echo "${FIRST_PATH}" | grep -q "^/pvc/${PVC}\($\|/\)"; then
                SRC_ROOT="/pvc/${PVC}"
              else
                SRC_ROOT="${FIRST_PATH}"
              fi
              ;;
          esac

          echo "Detected first path: ${FIRST_PATH}"
          echo "Source mount root: ${SRC_ROOT}"
          echo "Target mount root: ${TARGET_ROOT}"

          mode_to_octal() {
            p="$1"
            u=0; g=0; o=0
            [ "$(printf '%s' "$p" | cut -c2)" = "r" ] && u=$((u+4))
            [ "$(printf '%s' "$p" | cut -c3)" = "w" ] && u=$((u+2))
            case "$(printf '%s' "$p" | cut -c4)" in x|s|t) u=$((u+1));; esac
            [ "$(printf '%s' "$p" | cut -c5)" = "r" ] && g=$((g+4))
            [ "$(printf '%s' "$p" | cut -c6)" = "w" ] && g=$((g+2))
            case "$(printf '%s' "$p" | cut -c7)" in x|s|t) g=$((g+1));; esac
            [ "$(printf '%s' "$p" | cut -c8)" = "r" ] && o=$((o+4))
            [ "$(printf '%s' "$p" | cut -c9)" = "w" ] && o=$((o+2))
            case "$(printf '%s' "$p" | cut -c10)" in x|s|t) o=$((o+1));; esac
            echo "${u}${g}${o}"
          }

          rebase_path() {
            src="$1"
            root="$2"
            target="$3"

            if [ "${src}" = "${root}" ]; then
              echo "${target}"
              return
            fi

            case "${src}" in
              "${root}"/*)
                rel="${src#${root}/}"
                echo "${target}/${rel}"
                return
                ;;
            esac

            # Fallbacks for common temp-mount captures.
            if [ "${src}" = "/data" ] || [ "${src}" = "/target" ]; then
              echo "${target}"
              return
            fi
            case "${src}" in
              /data/*)
                rel="${src#/data/}"
                echo "${target}/${rel}"
                return
                ;;
              /target/*)
                rel="${src#/target/}"
                echo "${target}/${rel}"
                return
                ;;
            esac

            echo ""
          }

          format="STANDARD"
          if head -n 1 "${PERMS_FILE}" | grep -q 'FORMAT:LS_LDN'; then
            format="LS_LDN"
          fi
          echo "Format: ${format}"
          sample=$(grep -v '^#' "${PERMS_FILE}" | grep -v '^total' | head -n 1 || true)
          echo "Sample line: ${sample}"

          applied=0
          missing=0
          skipped=0
          errors=0
          total=0
          shown=0

          while IFS= read -r line; do
            [ -n "${line}" ] || continue
            case "${line}" in \#*|total*) continue ;; esac
            total=$((total+1))

            if [ "${format}" = "LS_LDN" ]; then
              mode_txt=$(echo "${line}" | awk '{print $1}')
              uid=$(echo "${line}" | awk '{print $3}')
              gid=$(echo "${line}" | awk '{print $4}')
              path=$(echo "${line}" | awk '{for(i=9;i<=NF;i++){printf "%s", $i; if(i<NF) printf " "}; print ""}')
              mode=$(mode_to_octal "${mode_txt}")
            else
              mode=$(echo "${line}" | awk '{print $1}')
              uid=$(echo "${line}" | awk '{print $2}')
              gid=$(echo "${line}" | awk '{print $3}')
              path=$(echo "${line}" | awk '{for(i=5;i<=NF;i++){printf "%s", $i; if(i<NF) printf " "}; print ""}')
              if [ -z "${path}" ]; then
                path=$(echo "${line}" | awk '{print $4}')
              fi
            fi

            [ -n "${path}" ] || { skipped=$((skipped+1)); continue; }
            target_path=$(rebase_path "${path}" "${SRC_ROOT}" "${TARGET_ROOT}")
            [ -n "${target_path}" ] || { skipped=$((skipped+1)); continue; }

            if [ "${shown}" -lt 5 ]; then
              echo "Rebase sample: ${path} -> ${target_path}"
              shown=$((shown+1))
            fi

            if [ -e "${target_path}" ]; then
              chown "${uid}:${gid}" "${target_path}" 2>/dev/null || errors=$((errors+1))
              chmod "${mode}" "${target_path}" 2>/dev/null || errors=$((errors+1))
              applied=$((applied+1))
            else
              missing=$((missing+1))
            fi
          done < "${PERMS_FILE}"

          # Post-restore hardening for known SAS Viya stateful roots.
          case "${PVC}" in
            sas-crunchy-platform-postgres-*-pgdata|*pgdata)
              chown -R 26:26 /target 2>/dev/null || true
              chmod 700 /target 2>/dev/null || true
              ;;
            sas-crunchy-platform-postgres-repo1|*repo1)
              chown root:root /target 2>/dev/null || true
              chmod 777 /target 2>/dev/null || true
              [ -d /target/archive ] && chown -R 26:26 /target/archive && find /target/archive -type d -exec chmod 750 {} + && find /target/archive -type f -exec chmod 640 {} + || true
              [ -d /target/backup ] && chown -R 26:26 /target/backup && find /target/backup -type d -exec chmod 750 {} + && find /target/backup -type f -exec chmod 640 {} + || true
              [ -d /target/log ] && chown -R 26:26 /target/log && find /target/log -type d -exec chmod 775 {} + && find /target/log -type f -exec chmod 664 {} + || true
              ;;
            sas-viya-consul-data-volume-*|*consul*)
              chown -R 1001:1001 /target 2>/dev/null || true
              ;;
            sas-viya-rabbitmq-data-volume-*|*rabbitmq*)
              chown -R 1001:1001 /target 2>/dev/null || true
              ;;
            sas-viya-redis-data-volume-*|*redis*)
              chown -R 1001:1001 /target 2>/dev/null || true
              ;;
            cas-default-data|cas-default-permstore|sas-cas-backup-data|sas-quality-knowledge-base)
              if [ "${FORCE_CAS_OWNERSHIP}" = "true" ]; then
                echo "Applying CAS/RWX ownership hardening: chown -R 1001:1001 /target"
                chown -R 1001:1001 /target 2>/dev/null || true
                chmod -R u+rwX,g+rwX /target 2>/dev/null || true
              fi
              ;;
          esac

          echo "Applied: ${applied}/${total}, missing: ${missing}, skipped: ${skipped}, errors: ${errors}"

          if [ "${total}" -gt 0 ] && [ "${applied}" -eq 0 ]; then
            echo "[FAIL] 0 permissions applied. Path rebasing or restored data is wrong for ${PVC}."
            exit 2
          fi
          if [ "${errors}" -gt 0 ]; then
            echo "[WARN] Completed with chmod/chown errors."
            exit 3
          fi
          echo "[PASS] Permissions restored for ${PVC}"
        volumeMounts:
        - name: backup
          mountPath: /backup
        - name: target
          mountPath: /target
      volumes:
      - name: backup
        persistentVolumeClaim:
          claimName: __BACKUP_PVC__
      - name: target
        persistentVolumeClaim:
          claimName: __PVC__
YAML

  sed -i \
    -e "s|__JOB_NAME__|${job}|g" \
    -e "s|__NAMESPACE__|${NAMESPACE}|g" \
    -e "s|__BACKUP_PVC__|${BACKUP_PVC}|g" \
    -e "s|__PVC__|${pvc}|g" \
    -e "s|__ACCESS_MODE__|${access_mode}|g" \
    -e "s|__FORCE_CAS_OWNERSHIP__|${FORCE_CAS_OWNERSHIP}|g" \
    "${job_file}"

  kubectl apply -f "${job_file}"
  rm -f "${job_file}"

  if kubectl wait --for=condition=complete -n "${NAMESPACE}" job/"${job}" --timeout=1800s >/dev/null 2>&1; then
    kubectl logs -n "${NAMESPACE}" job/"${job}" || true
    echo "[PASS] ${pvc}"
    ok=$((ok+1))
  else
    logs=$(kubectl logs -n "${NAMESPACE}" job/"${job}" 2>&1 || true)
    echo "${logs}"
    if echo "${logs}" | grep -q '\[SKIP\] No permission file'; then
      echo "[SKIP] ${pvc}"
      skip=$((skip+1))
    else
      echo "[FAIL] ${pvc} - check logs above"
      fail=$((fail+1))
    fi
  fi
  echo ""
done

# Clean stale workload-orchestrator services where their endpoint object has no backing addresses.
if [ "${CLEANUP_STALE_WO_SERVICES}" = "true" ]; then
  echo "=============================================="
  echo "Cleaning stale workload-orchestrator services with empty endpoints"
  echo "=============================================="
  for svc in $(kubectl get svc -n "${NAMESPACE}" -o name | sed 's#service/##' | grep '^sas-workload-orchestrator' || true); do
    endpoints=$(kubectl get endpoints "${svc}" -n "${NAMESPACE}" -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null || true)
    if [ -z "${endpoints}" ]; then
      echo "Deleting stale service with no endpoints: ${svc}"
      kubectl delete svc "${svc}" -n "${NAMESPACE}" --ignore-not-found=true
    else
      echo "Keeping service with endpoints: ${svc} -> ${endpoints}"
    fi
  done
  echo ""
fi

if [ "${RESTART_AFTER}" = "true" ]; then
  echo "=============================================="
  echo "Restarting stateful/CAS pods after permission restore"
  echo "=============================================="
  kubectl delete pod -n "${NAMESPACE}" -l app=sas-consul-server --ignore-not-found=true || true
  kubectl delete pod -n "${NAMESPACE}" -l app.kubernetes.io/name=sas-rabbitmq-server --ignore-not-found=true || true
  kubectl delete pod -n "${NAMESPACE}" -l app=sas-redis-server --ignore-not-found=true || true
  # CAS controller pod is often named exactly sas-cas-server-default-controller.
  kubectl delete pod -n "${NAMESPACE}" sas-cas-server-default-controller --ignore-not-found=true --force --grace-period=0 || true
  echo "Restart requests submitted."
  echo ""
fi

if [ "${CLEANUP_JOBS_AFTER}" = "true" ]; then
  echo "=============================================="
  echo "Cleaning permission restore jobs"
  echo "=============================================="
  kubectl delete job -n "${NAMESPACE}" -l app=viya-perm-restore-v12 --ignore-not-found=true || true
  kubectl delete job -n "${NAMESPACE}" -l app=viya-perm-restore-v11 --ignore-not-found=true || true
  kubectl delete job -n "${NAMESPACE}" -l app=viya-perm-restore --ignore-not-found=true || true
  echo ""
fi

echo "=============================================="
echo "Permission restore v12 summary"
echo "PASS: ${ok}"
echo "SKIP: ${skip}"
echo "FAIL: ${fail}"
echo "=============================================="
echo ""
echo "Post-check commands:"
echo "kubectl get pods -n ${NAMESPACE} | egrep 'cas|crunchy|consul|rabbit|redis'"
echo "kubectl get endpoints -n ${NAMESPACE} | egrep 'cas|data-quality|qkb|workload-orchestrator'"
echo "kubectl logs -n ${NAMESPACE} deployment/sas-readiness | tail -30"

if [ "${fail}" -gt 0 ]; then
  exit 1
fi
