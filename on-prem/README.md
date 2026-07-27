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

```bash
./viya-dr-automation-hpos --restore
./viya-dr-automation-hpos --restore --debug
```

Restore workflow sequence:

1. Start Velero restore.
2. Complete restore describe/verification step.
3. Run `scripts/restore_permission.sh`.
4. Print a status summary table and final summary.

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

- Permission Backup
- Restore Permissions
- Overall DR Operation

## Destroy Cleanup Operations

Use destroy cleanup to tear down Velero resources and the configured libreFS bucket.

```bash
./viya-dr-automation-hpos --destroy
# alias:
./viya-dr-automation-hpos --cleanup
```

Destroy cleanup behavior:

- Removes Velero resources in the configured `VELERO_NAMESPACE`.
- Deletes all objects from `LIBREFS_BUCKET` and then deletes the bucket.
- Is idempotent and safe to rerun.
- Continues when resources are already missing.
- Prints a cleanup report in this format:

```text
<Resource> | <Type> | <Action> | <Status>
```

Status values:

- `Deleted`
- `Not Found`
- `Skipped`
- `Failed`

A final cleanup summary is printed as one of:

- `Success`
- `Partial Success`
- `Failed`

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
