# Restore Folder

This folder contains templates and configurations for Velero restore operations.

## Files:
- `restore-template.yaml`: Template for creating Velero restore configurations
- `azure-velero-credentials`: Copy of credentials file for restore cluster installations
- `velero-priorityclass.yaml`: High-priority class configuration for Velero node-agent on restore clusters

## Priority Class Configuration:
The restore cluster includes a special priority class configuration for the Velero node-agent:
- **Priority Value**: 1000000 (high priority)
- **Purpose**: Ensures Velero node-agent has priority during restore operations
- **Scope**: Applied only to restore clusters (not source clusters)

## Usage:
This folder is used when setting up restore operations on a target AKS cluster, allowing you to restore backups created from a source cluster. The priority class ensures stable restore operations even when cluster resources are constrained during the restore process.

## Restore Process:
1. Credentials are automatically copied from source cluster setup
2. Priority class is created for node-agent stability
3. Velero is installed with identical configuration to source cluster
4. Node-agent is patched with both tolerations and priority class
5. Restore templates are processed for actual restore operations