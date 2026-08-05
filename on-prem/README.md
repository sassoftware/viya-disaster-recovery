# Velero HPOS Automation for SAS Viya 4 Disaster Recovery

This folder follows the Azure automation style, but targets HPOS/OpenStack using libreFS as an S3-compatible object store and Velero's AWS plugin.

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
3. Wait at least 40 seconds after `BackupStorageLocation` is `Available`.
4. Refresh and fetch available backups from Velero, retrying several times if backup discovery is still in progress.
5. List available backups.
6. Prompt to select a backup and validate the selected backup is in `Completed` phase.
7. Start Velero restore with the selected backup and monitor until terminal phase.
8. Run `scripts/restore_permission.sh` only after successful restore completion.
9. Print progress, status summary, and final summary.

If permission restore fails, diagnostics and exit code are reported clearly.

## Permission Script Files

The following scripts are integrated into the DR workflow:

- `scripts/backup_permission.sh` (canonical entrypoint)
- `scripts/restore_permission.sh` (canonical entrypoint)

Execution prerequisites validated by the automation:

- Script file exists
- Script file is executable
- Required parameters are present

Status summary format:

```text
Component | Phase | Action | Status | Exit Code
```

Final summary fields:

- Backup flow:
	- Permission Backup
	- Restore Permissions
	- Overall DR Operation
- Restore flow:
	- Restore Permissions
	- Overall DR Operation

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

BACKUP_NAME=viya-full-backup
RESTORE_NAME=viya-restore
CREDENTIALS_DIR=credentials
CREDENTIALS_FILE=velero-creds-local
```

## Notes

- `LIBREFS_INSTALL_MODE=skip` assumes libreFS is already running and reachable.
- `LIBREFS_INSTALL_MODE=local` installs libreFS on the current machine using sudo.
- `LIBREFS_INSTALL_MODE=remote` copies the libreFS binary to `LIBREFS_REMOTE_HOST` and configures systemd over SSH.
- Velero is installed with `--features=EnableCSI`, `--use-node-agent`, and `--default-snapshot-move-data`.
