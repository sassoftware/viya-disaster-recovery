# Velero Backup Configuration

This directory contains backup configurations for Velero to backup SAS Viya namespaces on AWS EKS.

## Files

### `backup-template.yaml`
Template file for creating Velero backup configurations. Contains placeholders that get replaced with actual values during backup execution:
- `BACKUP_NAME_PLACEHOLDER` — replaced with a timestamped backup name (e.g. `viya-backup-20260504-103045`)
- `NAMESPACE_PLACEHOLDER` — replaced with the Viya namespace selected at runtime

## Backup Configuration Details

- **Snapshot Volumes**: Enabled — creates EBS volume snapshots via CSI driver
- **CSI Snapshot Timeout**: 4 hours (configurable for large volumes)
- **Include Cluster Resources**: Yes
- **TTL (Time To Live)**: 720 hours (30 days)
- **Default Volume Backup Method**: CSI snapshots; NFS/EFS volumes fall back to filesystem backup (Kopia)

## Usage

The backup process is orchestrated by the main automation tool. Before running backup, ensure:
1. `CLUSTER_TYPE=source` is set in `environment.properties`
2. Velero is installed on the source cluster (`./aws-viya4-dr --steps velero`)
3. EFS/NFS volume permissions have been captured (run automatically by `--backup`)

```bash
# Execute backup (also runs backup-permissions.sh before the Velero snapshot)
./aws-viya4-dr --backup
```

### What Happens During Backup

1. Prompts for (or reads from config) the Viya namespace
2. **Runs `scripts/backup-permissions.sh`** — captures EFS/NFS volume POSIX permissions to a PVC
3. Creates Velero backup from this template with EBS CSI snapshots
4. Stores backup name in `state.json` for use during restore
