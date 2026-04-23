# Velero Backup Configuration

This directory contains backup configurations for Velero to backup SAS Viya namespaces.

## Files

### `backup-template.yaml`
Template file for creating Velero backup configurations. Contains placeholder `NAMESPACE_PLACEHOLDER` that gets replaced with the actual namespace during backup execution.

### Generated Backup Files
When executing backups, timestamped backup files are generated in this directory with the format:
`backup-{namespace}-{timestamp}.yaml`

Example: `backup-viya-20241007-143052.yaml`

## Backup Configuration Details

- **Backup Name**: `va-viya-pg-cas-backup`
- **Snapshot Volumes**: Enabled for persistent volume backups
- **CSI Snapshot Timeout**: 2 hours
- **Include Cluster Resources**: Yes
- **TTL (Time To Live)**: 720 hours (30 days)
- **Default Volume Backup Method**: Snapshots (not file-system backup)

## Usage

The backup process is orchestrated by the main automation tool:

```powershell
# Execute backup process
go run *.go --steps=backup

# This will:
# 1. Validate Velero installation and readiness
# 2. Check backup location availability  
# 3. Prompt for Viya namespace name
# 4. Create backup configuration file
# 5. Execute backup using kubectl apply
# 6. Monitor backup progress until completion
```

## Pre-requisites

Before running backup:
1. Velero must be installed and running
2. Node-agent must be configured and ready
3. Backup storage location must be available
4. Target namespace must exist in the cluster

## Backup Process Flow

1. **Validation Phase**
   - Check Velero pods are running
   - Verify node-agent daemonset is ready
   - Validate backup storage location is available

2. **Configuration Phase**
   - Prompt user for Viya namespace name (default: viya)
   - Validate specified namespace exists
   - Generate backup configuration file from template

3. **Execution Phase**
   - Apply backup configuration to cluster
   - Monitor backup progress with 30-second intervals
   - Display backup completion status and details

4. **Monitoring**
   - Maximum wait time: 30 minutes
   - Progress updates every 30 seconds
   - Final backup details and logs

## Backup File Structure

```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: va-viya-pg-cas-backup
  namespace: velero
spec:
  includedNamespaces:
    - {your-namespace}  # User-specified namespace
  snapshotVolumes: true
  defaultVolumesToFsBackup: false
  csiSnapshotTimeout: 2h
  includeClusterResources: true
  ttl: 720h0m0s
```