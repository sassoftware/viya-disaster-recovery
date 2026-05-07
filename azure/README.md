# Velero Azure Automation for SAS Viya 4 Disaster Recovery

[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org)
[![Azure CLI](https://img.shields.io/badge/Azure%20CLI-2.0+-blue.svg)](https://docs.microsoft.com/en-us/cli/azure/)
[![kubectl](https://img.shields.io/badge/kubectl-1.24+-blue.svg)](https://kubernetes.io/docs/tasks/tools/)

> **⚠️ Important:** This is the Azure-specific implementation located in the `azure/` directory. All commands in this README must be run from the `azure/` directory.

Comprehensive automation suite for deploying Velero on Azure Kubernetes Service
(AKS) clusters, enabling disaster recovery for SAS Viya 4 Non-Multi-Tenant (NMT)
deployments. This solution includes two main tools: the core Velero automation
CLI and an independent SAS Viya health checker.

## Table of Contents

- [Repository Overview](#repository-overview)
- [System Requirements](#system-requirements)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Operations](#operations)
  - [1. Setup](#1-setup-operations)
  - [2. Backup](#2-backup-operations)
  - [3. Restore](#3-restore-operations)
  - [4. Post-Restore: Update Ingress](#4-post-restore-update-ingress-for-dr-cluster)
- [Cleanup Operations](#cleanup-operations)
- [SAS Viya Health Checker](#sas-viya-health-checker-independent-tool)
- [Debugging & Troubleshooting](#debugging--troubleshooting)

## Repository Overview

- ✅ **Automated Azure Infrastructure**: Creates storage accounts, resource groups, service principals with proper RBAC
- ✅ **Kubernetes Integration**: Deploys CSI snapshot classes for Azure disk and NFS volumes
- ✅ **Velero Management**: Automatic installation, configuration, and verification
- ✅ **Cross-Cluster Restore**: Complete restore workflow with CSI driver patches and priority class configuration
- ✅ **Independent Health Monitoring**: Comprehensive SAS Viya health checks with detailed reporting
- ✅ **Configuration Management**: External configuration via `environment.properties` file
- ✅ **State Persistence**: Resumable operations with JSON-based state tracking
- ✅ **Comprehensive Validation**: Pre-flight checks and environment validation

## System Requirements

### Workstation Requirements

> Run this tool from a Linux workstation that has direct network access to your AKS cluster.

| | | Minimum | Recommended |
|-|-|---------|-------------|
| OS | | Rocky Linux 9.x, RHEL 9.x, CentOS Stream 9 | Rocky Linux 9.x or RHEL 9.x |
| CPU | Cores | 2 cores | 4 cores |
| RAM | Memory | 4 GB | 8 GB |
| Disk | Free space | 10 GB | 20 GB |
| Network | Connectivity | Access to AKS API server | Low-latency connection to Azure |

### Required Tools

| Tool | Minimum Version | Installation |
|------|----------------|--------------|
| **Go** | 1.21+ | [Download Go](https://golang.org/dl/) |
| **Azure CLI** | 2.0+ | [Install Azure CLI](https://docs.microsoft.com/en-us/cli/azure/install-azure-cli) |
| **kubectl** | 1.24+ | [Install kubectl](https://kubernetes.io/docs/tasks/tools/) |
| **Velero CLI** | 1.17+ | [Install Velero](https://velero.io/docs/main/basic-install/) |

### Supported Versions

| Component | Version | Notes |
|-----------|---------|-------|
| Kubernetes | 1.24+ | AKS cluster version |
| AKS | Latest stable | Azure Kubernetes Service |
| SAS Viya 4 | Tested on and above Stable 2025.09 | Non-Multi-Tenant (NMT) only |
| Velero | 1.17+ | Installed by automation |
| Velero Azure Plugin | 1.10+ | Installed by automation |

### Azure Subscription Access

| Role | Requirement |
|------|-------------|
| Azure Subscription Role | **Owner** — Required to assign RBAC roles, create resource groups, storage accounts, and service principals |
| Azure AD Role | **Application Administrator** or **Global Administrator** — Required to register and manage service principals used by Velero |

```bash
# To verify your current subscription role:
az role assignment list --assignee $(az ad signed-in-user show --query id -o tsv) \
  --scope /subscriptions/<SUBSCRIPTION_ID> \
  --query "[].{Role:roleDefinitionName}" -o table
# Should show: Owner
```

### Installation Commands

```bash
# Go — CentOS/RHEL/Rocky Linux
sudo dnf install golang

# Azure CLI — CentOS/RHEL/Rocky Linux
sudo dnf install azure-cli

# kubectl
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl

# Velero CLI
wget https://github.com/vmware-tanzu/velero/releases/download/v1.17.2/velero-v1.17.2-linux-amd64.tar.gz
tar -xzf velero-v1.17.2-linux-amd64.tar.gz
sudo mv velero-v1.17.2-linux-amd64/velero /usr/local/bin/

# Verify all installations
go version && az --version && kubectl version --client && velero version --client-only
```

## Quick Start

### 1. Clone and Build

```bash
git clone <repository-url>
cd viya-disaster-recovery/azure

# Build main Velero automation CLI
go build -o viya-dr-automation-azure .

# Build independent health checker
go build -tags health -o viya-health-checker viya_health_minimal.go

# Make both executable and verify
chmod +x viya-dr-automation-azure viya-health-checker
./viya-dr-automation-azure --help
./viya-health-checker --help
```

### Available CLI Tools After Build

| Tool | Purpose | Binary | Usage |
|------|---------|--------|-------|
| **Velero Automation CLI** | Complete disaster recovery automation | `viya-dr-automation-azure` | Backup, restore, infrastructure setup |
| **SAS Viya Health Checker** | Independent health monitoring | `viya4-healthcheck` | Health checks, system validation |

### 2. Azure Authentication

> **Disclaimer**: This automation only requires your Azure subscription ID for resource management. No user credentials (username/password) are stored or required in the configuration. It assumes you already have appropriate access to the Azure subscription being used.

> **Service Principal**: We create a service principal as part of the DR process which will be used by Velero. Make sure you have access to create service principals in the used subscription (requires Application Administrator or Global Administrator role in Azure AD).

```bash
# Login to Azure
az login

# Set subscription (if multiple)
az account set --subscription "your-subscription-id"

# Verify authentication
az account show
```

## Configuration

### Set Up `environment.properties`

> **⚠️ Important:** Before running the automation tool, copy the example file and update all variables to match your environment. The default values are placeholders and must be replaced with your actual Azure subscription details, cluster names, resource group names, and kubeconfig paths.

```bash
cp environment.properties.example environment.properties
nano environment.properties
```

### Kubeconfig Requirements

> **⚠️** A valid kubeconfig file must be provided for backup or restore operations.

- The automation assumes that the provided kubeconfig contains configuration for a single target Kubernetes cluster only.
- Kubeconfig files containing multiple cluster contexts (multiple clusters, users, or contexts) are **not supported**.
- If the kubeconfig contains multiple contexts, the automation may operate against an unintended cluster, leading to unpredictable results.

### Complete Configuration Reference

> **⚠️ Storage Account Name must be globally unique in Azure:** `VELERO_STORE_NAME` is used to create an Azure Storage Account, which requires a globally unique name across all of Azure (3–24 lowercase alphanumeric characters, no hyphens or underscores). The default value `psvelerostore` may already be taken. Use a name specific to your organisation, e.g. `myorgvelerostore` or `acmedr2026store`.

```properties
# Azure Configuration
VELERO_STORE_NAME=psvelerostore        # Storage account name (globally unique)
VELERO_BLOB_RG=psvelerostorerg         # Storage resource group
VELERO_SNAP_RG=psvelerosnapsrg         # Snapshot resource group
LOCATION=eastus                        # Azure region

# Cluster Configuration
CLUSTER_TYPE=source                    # source|restore
VIYA_DEPLOYMENT_TYPE=nmt               # nmt|mt (only nmt supported)
KUBECONFIG_PATH=/path/to/kubeconfig    # Cluster kubeconfig path

# NFS Storage Class
NFS_STORAGE_CLASS=sas                  # Storage class for NFS permission backup PVC

# AKS MC Resource Groups (Format: MC_{rg}_{cluster-name}_{region})
SOURCE_MC_RESOURCE_GROUP=MC_source-rg_source-cluster_eastus

# Azure subscription ID
MANUAL_SUBSCRIPTION_ID=your-subscription-id
```

### Variable Changes by Operation

| Operation | CLUSTER_TYPE | KUBECONFIG_PATH | Notes |
|-----------|--------------|-----------------|-------|
| **Setup** | `source` | Source cluster path | Initial infrastructure provisioning |
| **Backup** | `source` | Source cluster path | Run from source cluster |
| **Restore** | `restore` | Target/DR cluster path | See Restore Pre-Requisites |

## Operations

### 1. Setup Operations

#### Option 1: Individual Steps (Recommended)

```bash
# Step 1 — Provision Azure resources
# Creates: storage account, resource groups, service principal, RBAC assignments
./viya-dr-automation-azure --steps=azure

# Step 2 — Configure Kubernetes
# Applies: CSI snapshot classes for Azure disk and NFS volumes
./viya-dr-automation-azure --steps=kubernetes

# Step 3 — Install and configure Velero
# Installs: Velero with Azure plugin using the service principal from Step 1
./viya-dr-automation-azure --steps=velero
```

> Use individual steps to run each phase separately with full visibility and the ability to re-run a single step if a failure occurs.

#### Option 2: Full Setup (All steps at once)

```bash
./viya-dr-automation-azure --steps=azure,kubernetes,velero
```

> Use this when you are confident in the configuration and want to complete the entire setup without interruption.

### 2. Backup Operations

> **⚠️ Important – Generated Credentials:** During backup execution, the automation dynamically generates cloud-specific credentials required to access the backup storage. These credentials are created locally under the `credentials/` directory in the same path from which the backup command is executed.
>
> Preserve the `credentials/` directory and `state.json` after backup completion. They are required for any subsequent restore operation using the same backup.

#### Execute Backup

```bash
./viya-dr-automation-azure --backup

# With debug logging
./viya-dr-automation-azure --backup --debug
```

#### IAC/DAC Deployments — Backup Order

> If your AKS cluster was provisioned using IAC (Infrastructure as Code) and SAS Viya was deployed using DAC (Deployment as Code), you must take two separate backups in the following order. The `sasoperator` namespace must be backed up first.

Step 1 — Backup the `sasoperator` namespace:

```bash
./viya-dr-automation-azure --backup
# When prompted for namespace, select: sasoperator (or sas-deployment-operator)
# Wait for the backup to complete before proceeding
```

Step 2 — Backup the Viya namespace:

```bash
./viya-dr-automation-azure --backup
# When prompted for namespace, select: your Viya namespace (e.g. viya4)
# Wait for the backup to complete before proceeding
```

Step 3 — Verify both backups completed successfully:

```bash
velero backup get
```

Expected output:
```
NAME                 STATUS      ERRORS   WARNINGS   CREATED                         EXPIRES   ...   NAMESPACES
sasoperator-backup   Completed   0        0          2026-03-27 10:00:00 +0000 UTC   29d       ...   sasoperator
viya-backup          Completed   0        0          2026-03-27 10:05:00 +0000 UTC   29d       ...   <VIYA_NAMESPACE>
```

> ✅ Both backups must show **Completed** status with 0 errors before proceeding to a restore.

### 3. Restore Operations

#### Target Cluster Pre-Requisites

> **⚠️** This tooling does not restore a running SAS Viya environment into another running SAS Viya environment. The Velero restore process creates the Viya namespace and all of its contents from scratch on the DR cluster. There must be no existing SAS Viya deployment in the target namespace before the restore is run.

> **🔒** The DR/target AKS cluster must reside in the **same Azure subscription** as the source cluster. Cross-subscription restore has not been tested.

Before running any restore command, the DR/target AKS cluster must already have the following cluster-level infrastructure in place:

| Component | Namespace | Why Required |
|-----------|-----------|--------------|
| Ingress controller (nginx) | `ingress-nginx` | Velero restores Viya Ingress/HTTPProxy objects — the controller must exist to serve them |
| NFS CSI driver | `kube-system` | Required to provision and mount NFS-backed PersistentVolumes restored by Velero |
| Azure Disk CSI driver | `kube-system` | Required to provision and mount Azure Disk PersistentVolumes restored by Velero |
| NFS StorageClass | cluster-scoped | Viya PVCs reference a named storage class — it must exist with the same name as on the source cluster |
| Azure Disk StorageClass | cluster-scoped | Same requirement as above for disk-based PVCs |
| cert-manager (if TLS is used) | `cert-manager` | Certificate Issuer/ClusterIssuer resources restored by Velero require cert-manager to be running |
| SAS Deployment Operator (sasoperator) | `sasoperator` | Required for Viya CRD reconciliation — must be restored or pre-installed before the Viya namespace restore |

```bash
# Verify prerequisites are in place:
kubectl get pods -n ingress-nginx                              # nginx ingress
kubectl get pods -n kube-system -l app=csi-nfs-node           # NFS CSI driver
kubectl get pods -n kube-system -l app=azuredisk-csi-node     # Azure Disk CSI driver
kubectl get storageclass                                       # Storage classes (names must match source cluster)
kubectl get pods -n cert-manager                              # cert-manager (if applicable)
kubectl get pods -n sasoperator                               # SAS Deployment Operator
```

> ✅ All required components must show **Running** pods before you proceed. The restore will fail silently or leave PVCs in `Pending` state if the CSI drivers or storage classes are missing.

If you used DAC to deploy the source environment, run the baseline playbook on the DR cluster to provision all components above:

```bash
# Adjust the playbook name and path to your DAC deployment directory
ansible-playbook deploy_viya.yml -e 'DEPLOY_BASELINE=true'
```

> ✅ Wait for the baseline to complete fully before proceeding.

#### Pre-Restore Requirements — Read Before Running

**Requirement 1 — Run from the same working directory as backup**

> 🗂️ The restore command must be executed from the **exact same working directory** in which the backup was originally run.

During backup, the automation creates a `credentials/` subdirectory containing Azure service principal credentials. The restore operation uses these same credentials.

```
<working-directory>/
├── viya-dr-automation-azure
├── environment.properties
├── credentials/
│   └── credentials-velero.json    ← generated during backup, required for restore
└── state.json
```

**Requirements 2–4 — Update `environment.properties` before running on the DR cluster**

- Set `CLUSTER_TYPE=restore` (was `source` on the backup cluster)
- Set `KUBECONFIG_PATH` to the DR cluster admin kubeconfig — not the source cluster
- Confirm the DR cluster is in the same Azure subscription as the source cluster (`az account show --query id -o tsv`)

> **⚠️** Additionally, `VELERO_STORE_NAME` and `VELERO_BLOB_RG` must **exactly match** the values used during backup on the source cluster.

Sample `environment.properties` for Restore:

```properties
# =======================================================
# environment.properties — DR Cluster (Restore)
# =======================================================

# Azure Blob Storage — MUST match backup cluster values exactly
VELERO_STORE_NAME=psvelerostore
VELERO_BLOB_RG=psvelerostorerg
VELERO_SNAP_RG=psvelerosnapsrg
VELERO_SERVICE_PRINCIPAL_NAME=velero-test

# Azure region — MUST match backup cluster value
LOCATION=eastus

# *** CHANGE FOR RESTORE *** — must be "restore" (was "source" on backup cluster)
CLUSTER_TYPE=restore

# Only "nmt" supported
VIYA_DEPLOYMENT_TYPE=nmt

# *** CHANGE FOR RESTORE *** — must point to DR cluster kubeconfig (not source cluster)
KUBECONFIG_PATH=/path/to/dr-cluster/admin-kubeconfig

# Storage class used for the NFS permission backup PVC
NFS_STORAGE_CLASS=sas

# AKS MC Resource Groups (Format: MC_{rg}_{cluster-name}_{region})
SOURCE_MC_RESOURCE_GROUP=MC_your-source-rg_your-source-cluster_eastus

# *** CHANGE FOR RESTORE *** — must be the same subscription that owns both clusters
MANUAL_SUBSCRIPTION_ID=XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX
```

#### Execute Restore

```bash
# Run restore on DR cluster (from the same directory backup was run)
./viya-dr-automation-azure --restore

# With debug logging
./viya-dr-automation-azure --restore --debug
```

#### IAC/DAC Deployments — Restore Order

> If your DR cluster was provisioned using IAC and SAS Viya was originally deployed using DAC, follow these steps before and during restore. Skipping this order may cause CRD or operator dependency failures.

Step 1 — Apply DAC baseline on the DR cluster first:

> See [Target Cluster Pre-Requisites](#target-cluster-pre-requisites) above. The baseline must be fully complete before proceeding.

Step 2 — Restore the `sasoperator` namespace first:

```bash
./viya-dr-automation-azure --restore
# When prompted, select: sasoperator-backup (or deployment operator backup)
# Wait for the restore to complete, then validate:
kubectl get pods -n sasoperator
```

Step 3 — Then restore the Viya namespace:

```bash
./viya-dr-automation-azure --restore
# When prompted, select: viya-backup (or your Viya namespace backup name)
```

Step 4 — Verify both restores completed successfully:

```bash
velero restore get
```

Expected output:
```
NAME                    STATUS      ERRORS   WARNINGS   BACKUP
sasoperator-restore     Completed   0        0          sasoperator-backup
viya-restore            Completed   0        0          viya-backup
```

> ✅ Both restores must show **Completed** status with 0 errors before proceeding.

### 4. Post-Restore: Update Ingress for DR Cluster

After a Velero restore, all ingress resources in the Viya namespace still carry the source cluster's hostnames. How you handle this depends on your DR scenario.

You need to run the patch manually, use the provided helper script:

```bash
./scripts/update-ingress.sh
```

Verify the update:

```bash
kubectl get ingress -n <VIYA_NAMESPACE>
kubectl describe ingress -n <VIYA_NAMESPACE>
```

## Cleanup Operations

The `--cleanup` flag provides a safe, ordered uninstall of everything the automation created — across Azure, Kubernetes, and local files. Use it when:

- A setup run failed partially and you need to retry from a clean state
- You want to decommission the entire DR setup
- You need to rename resources (e.g. fix a storage account name conflict) and start fresh

> **⚠️** Cleanup is destructive and irreversible. It will delete Azure resource groups, the storage account (including all Velero backups stored in it), the service principal, and all Kubernetes resources. You will be prompted to confirm before anything is deleted.

### When to Use Cleanup

| Scenario | Action |
|----------|--------|
| Setup failed mid-way and you want a clean retry | Run `--cleanup`, then re-run setup |
| Storage account name conflict (globally taken) | Run `--cleanup`, update `VELERO_STORE_NAME`, retry |
| Config issue caused partial resource creation | Run `--cleanup` to remove partial resources, fix config, retry |
| Decommissioning the DR setup entirely | Run `--cleanup` |

### Run Cleanup

```bash
./viya-dr-automation-azure --cleanup
```

> **⚠️** You will be prompted to type `yes` to confirm before anything is deleted.

### What Gets Removed

Resources are removed in this order to avoid dependency failures:

| Order | Resource | Description |
|-------|----------|-------------|
| 1 | Velero Installation | Uninstalls Velero namespace, deployments, daemonsets |
| 2 | Service Principal | Deletes the Velero service principal and its RBAC assignments |
| 3 | Storage Account | Deletes the storage account and its blob container |
| 4 | Resource Groups | Deletes `VELERO_BLOB_RG` and `VELERO_SNAP_RG` and all contained resources |
| 5 | Kubernetes Resources | Removes CSI snapshot classes and related Kubernetes objects |
| 6 | Local Files | Removes `credentials/credentials-velero.json` and generated config files |

## SAS Viya Health Checker (Independent Tool)

The health checker is a standalone tool that provides comprehensive health monitoring for SAS Viya deployments without requiring Velero or backup operations.
Please refer to [`SAS Viya Health Checker`](https://github.com/sassoftware/blob/aws-support/README.md#-sas-viya-health-checker-standalone)

## Debugging & Troubleshooting

### Debug Mode

```bash
./viya-dr-automation-azure --backup --debug
./viya-dr-automation-azure --restore --debug
```

### Common Issues

1. **Authentication Failures**
   ```bash
   az login
   az account set --subscription "your-subscription-id"
   ```

2. **Kubeconfig Issues**
   ```bash
   # Verify cluster access
   kubectl --kubeconfig=/path/to/kubeconfig cluster-info
   # Then update KUBECONFIG_PATH in environment.properties
   ```

3. **Permission Errors**
   ```bash
   # Check Azure permissions
   az role assignment list --assignee $(az account show --query user.name -o tsv)

   # Verify Kubernetes RBAC
   kubectl auth can-i "*" "*" --all-namespaces
   ```

### State Management

```bash
cat state.json     # Check operation state
rm state.json      # Reset state (forces fresh start)
```

### Log Locations

| Source | Location |
|--------|----------|
| Application Logs | Console output |
| Velero Logs | `kubectl logs -n velero deployment/velero` |
| State File | `state.json` |
| Credentials | `credentials/credentials-velero.json` |
