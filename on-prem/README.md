# Onprem HPOS/Openstack Automation for SAS Viya 4 Disaster Recovery

This folder follows the Azure automation style, but targets HPOS/OpenStack using libreFS as an S3-compatible object store and Velero's AWS plugin.

Supports both Non-Multi-Tenant (NMT) and Multi-Tenant (MT) SAS Viya 4 deployments via `VIYA_DEPLOYMENT_TYPE`. MT support was validated through a DR POC against a limited SAS Viya 4 Multi-Tenant deployment provisioned with [UDANext Multi-Tenancy](https://github.com/sas-institute-rnd-internal/viya-udanext-mt); both deployment types run the identical Velero backup/restore workflow.

## Prerequisites

| Component | Version |
| --- | --- |
| Rocky Linux | 9.x |
| libreFS | Latest supported release |
| Velero CLI | v1.18.0 |
| Velero AWS Plugin | v1.12.1 |
| Kubernetes CSI External Snapshotter | v8.4.0 |
| NFS CSI Driver | `nfs.csi.k8s.io` |
| kubectl | Version compatible with the cluster |
| SAS Viya 4 Deployment Type | Non-Multi-Tenant (NMT) or Multi-Tenant (MT) |

## Build

```bash
go build -o viya-dr-automation-hpos
chmod +x viya-dr-automation-hpos
cp environment.properties.example environment.properties
```

Update `environment.properties` before running.

## Setup Operations

### Option 1: Individual steps

```bash
# Step 1 - Provision / configure HPOS object storage
# Installs libreFS as systemd service when LIBREFS_INSTALL_MODE=local or remote.
./viya-dr-automation-hpos --steps=hpos

# Step 2 - Configure Kubernetes
# Applies external snapshotter CRDs/controller and NFS VolumeSnapshotClass.
./viya-dr-automation-hpos --steps=kubernetes

# Step 3 - Install and configure Velero
# Installs Velero with AWS plugin pointing to libreFS endpoint.
./viya-dr-automation-hpos --steps=velero
```

### Option 2: Full setup

```bash
./viya-dr-automation-hpos --steps=hpos,kubernetes,velero
```

Use individual steps when you want full visibility and easy re-run of a failed phase.

## Backup Operations

```bash
./viya-dr-automation-hpos --backup
./viya-dr-automation-hpos --backup --debug
```

Backup workflow sequence:

1. Run `scripts/backup_permission.sh`.
2. Start Velero backup.
3. Print a status summary table and final summary.

If the permission backup script fails, backup stops and exits with failure.

The backup operation creates a local `credentials/` directory containing the S3 credentials file used by Velero. Preserve this directory and `state.json` for restore.

```text
<working-directory>/
├── viya-dr-automation-hpos
├── environment.properties
├── credentials/
│   └── velero-creds-local
└── state.json
```

## Restore Operations

Before restore, update `environment.properties`:

1. Set `CLUSTER_TYPE=restore`.
2. Set `KUBECONFIG_PATH` to the DR cluster admin kubeconfig, not the source cluster.
3. Keep `LIBREFS_ENDPOINT`, `LIBREFS_BUCKET`, `LIBREFS_ACCESS_KEY`, and `LIBREFS_SECRET_KEY` matching the source backup setup.
4. Run from the same working directory that contains `credentials/` and `state.json`, or explicitly set `BACKUP_NAME`.

Restore credential prerequisites:

- Restore requires an existing credentials file at `CREDENTIALS_DIR/CREDENTIALS_FILE`.
- Restore treats the credentials file as the source of truth.
- Restore reuses the existing backup/source-cluster credentials file and runs Velero install with `--secret-file`.
- Restore does not create a new credentials file path and uses the configured `CREDENTIALS_DIR/CREDENTIALS_FILE` values.

```bash
./viya-dr-automation-hpos --restore
./viya-dr-automation-hpos --restore --debug
```

Restore workflow sequence:

1. Install or verify Velero on the restore cluster.
2. Wait for `BackupStorageLocation` to be created and become `Available` (poll every 5 seconds, timeout 2 minutes).
3. Wait at least 50 seconds after `BackupStorageLocation` is `Available`.
4. Refresh and fetch available backups from Velero, retrying several times if backup discovery is still in progress.
5. List available backups.
6. Prompt to select a backup and validate the selected backup is in `Completed` phase.
7. Start Velero restore with the selected backup and monitor until terminal phase.
8. Run `scripts/restore_permission.sh` only after successful restore completion.
9. Print progress, status summary, and final summary.

If permission restore fails, diagnostics and exit code are reported clearly.

### Post-Restore Validation and Contour HTTPProxy Update

Once the restore has completed successfully, export the restored cluster configuration and verify that SAS Viya is running correctly.

```bash
export KUBECONFIG=/path/to/restored-cluster/admin-kubeconfig
kubectl config current-context
```

Validate that all Viya workloads are healthy and accessible. If SAS Viya is not fully operational, restart the environment using the following commands:

```bash
# Stop all Viya services
kubectl -n viya create job --from cronjobs/sas-stop-all stopdep-23062026

# Start all Viya services
kubectl -n viya create job --from cronjobs/sas-start-all startdep-23062026
```

Wait for the environment to become healthy before proceeding.

After SAS Viya is available, update the Contour HTTPProxy to use the restored cluster DNS.

Option 1: Automated update (recommended)

Use the built-in script to update `sas-httpproxy-root` non-interactively (export/update/apply) and validate the final HTTPProxy status.

```bash
# Run with interactive namespace prompt (default: viya)
./scripts/update_contour_httpproxy.sh

# Or pass namespace directly
./scripts/update_contour_httpproxy.sh viya
```

The script reads `environment.properties`, validates `CLUSTER_TYPE=restore`, exports `KUBECONFIG` from `KUBECONFIG_PATH`, computes the new FQDN (`<namespace>.contour.<cluster>`), applies the update, and verifies HTTPProxy `Valid` status.

Option 2: Manual update

Verify the current HTTPProxy configuration:

```bash
kubectl get httpproxy -n viya | grep sas-httpproxy-root
```

Example output:

```text
sas-httpproxy-root viya.contour.xxxx-source-m1.xxxx-iac-1.hpos5.xxx.xxx.com xxx-ingress-certificate-dmh6d8h7mh valid Valid HTTPProxy
```

Edit the HTTPProxy:

```bash
kubectl edit httpproxy sas-httpproxy-root -n viya
```

Update the `virtualhost.fqdn` value to the restored cluster DNS.

Example:

```yaml
virtualhost:
  fqdn: viya.contour.viya-restore-m1.xxxx-iac-1.hpos5.xxx.xxx.xxx
  tls:
    secretName: sas-ingress-certificate-dmh6d8h7mh
```

Save the changes and verify the HTTPProxy status:

```bash
kubectl get httpproxy -n viya | grep sas-httpproxy-root
```

Expected output:

```text
sas-httpproxy-root viya.contour.viya-restore-m1.xxxx-iac-1.hpos5.xxx.xxx.xxx xxx-ingress-certificate-dmh6d8h7mh valid Valid HTTPProxy
```

This step is required after a successful restore to ensure external access is routed through the restored cluster DNS and that the Contour HTTPProxy configuration is valid.

## Destroy Cleanup Operations

Use destroy cleanup to uninstall Velero and delete the Velero namespace.

```bash
./viya-dr-automation-hpos --destroy
# alias:
./viya-dr-automation-hpos --cleanup
```

Destroy cleanup behavior:

- Runs `velero uninstall --force` for the configured namespace.
- Deletes the configured `VELERO_NAMESPACE`.
- Waits until namespace deletion completes.
- Returns success when namespace deletion succeeds.

## Multi-Tenant (MT) Support

`VIYA_DEPLOYMENT_TYPE` accepts `nmt` (Non-Multi-Tenant) or `mt` (Multi-Tenant), case-insensitively. Both values run the identical validated Velero backup/restore workflow in this folder; the value is used only for startup validation and for the deployment type logged at the start of every run (`Detected VIYA_DEPLOYMENT_TYPE=...`). An invalid or missing value fails fast with a clear error before any DR operation starts.

For a Multi-Tenant deployment, set:

```properties
VIYA_NAMESPACE=mt
VIYA_DEPLOYMENT_TYPE=mt
```

`VIYA_NAMESPACE` typically matches the tenant/provider namespace created by the UDANext Multi-Tenancy enablement (commonly `mt`); update it to match your deployment. No other configuration changes are required for MT versus NMT.

## Sample restore environment.properties

```properties
KUBECONFIG_PATH=/path/to/dr-cluster/admin-kubeconfig
VIYA_NAMESPACE=viya
NFS_STORAGE_CLASS=sas
NFS_SNAPSHOT_CLASS=nfs-snapshot-class
SNAPSHOTTER_VERSION=v8.4.0
VIYA_DEPLOYMENT_TYPE=nmt
CLUSTER_TYPE=restore

LIBREFS_INSTALL_MODE=skip
LIBREFS_ENDPOINT=http://10.119.100.227:9000
LIBREFS_BUCKET=velero
LIBREFS_ACCESS_KEY=velero
LIBREFS_SECRET_KEY=velero12345

VELERO_NAMESPACE=velero
VELERO_PROVIDER=aws
VELERO_PLUGIN=velero/velero-plugin-for-aws:v1.12.1
VELERO_BUCKET=velero

BACKUP_NAME=auto
RESTORE_NAME=auto
CREDENTIALS_DIR=credentials
CREDENTIALS_FILE=velero-creds-local
```

## Notes

- `LIBREFS_INSTALL_MODE=skip` assumes libreFS is already running and reachable.
- `LIBREFS_INSTALL_MODE=local` installs libreFS on the current machine using sudo.
- `LIBREFS_INSTALL_MODE=remote` copies the libreFS binary to `LIBREFS_REMOTE_HOST` and configures systemd over SSH.
- Velero is installed with `--features=EnableCSI`, `--use-node-agent`, and `--default-snapshot-move-data`.
- `BACKUP_NAME=auto` and `RESTORE_NAME=auto` generate unique timestamped Velero names using `viya-full-backup-yyyyMMdd-HHmmss-nnnnnnnnn` and `viya-full-restore-yyyyMMdd-HHmmss-nnnnnnnnn`.
- `VIYA_DEPLOYMENT_TYPE=nmt` (single-tenant) and `VIYA_DEPLOYMENT_TYPE=mt` (multi-tenant) both run the identical validated on-prem DR workflow; the value only affects startup validation and logging, not the backup/restore behavior.
