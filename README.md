
# Velero Azure Automation for SAS Viya 4 Disaster Recovery

[![Go Version](https://img.shields.io/badge/Go-1.19+-blue.svg)](https://golang.org)
[![Azure CLI](https://img.shields.io/badge/Azure%20CLI-2.0+-blue.svg)](https://docs.microsoft.com/en-us/cli/azure/)
[![kubectl](https://img.shields.io/badge/kubectl-1.24+-blue.svg)](https://kubernetes.io/docs/tasks/tools/)

Comprehensive automation suite for deploying Velero on Azure Kubernetes Service (AKS) clusters, enabling disaster recovery for SAS Viya 4 Non-Multi-Tenant (NMT) deployments. This solution includes two main tools: the core Velero automation CLI and an independent SAS Viya health checker.

## Table of Contents

- [Repository Overview](#repository-overview)
- [System Requirements](#system-requirements)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Operations](#operations)
  - [1. Setup](#1-setup-operations)
  - [2. Backup](#2-backup-operations)
  - [3. Restore](#3-restore-operations)
    - [Target Cluster Pre-Requisites](#target-cluster-pre-requisites)
    - [Pre-Restore Requirements](#pre-restore-requirements-read-before-running)
    - [IAC/DAC Deployments — Restore Order](#iacdac-deployments--restore-order)
  - [4. Post-Restore: Update Ingress](#4-post-restore-update-ingress-for-dr-cluster)
    - [Choose Your Approach](#choose-your-approach)
    - [Option A — Update Ingress Hosts](#option-a--update-ingress-hosts-parallel--new-url)
    - [Option B — Switch DNS Alias](#option-b--switch-dns-alias-true-dr-failover)
- [Cleanup Operations](#cleanup-operations)
- [SAS Viya Health Checker](#sas-viya-health-checker-independent-tool)
- [Debugging & Troubleshooting](#debugging--troubleshooting)
- [Repository Structure](#repository-structure)
- [Security Considerations](#security-considerations)
- [Contributing](#contributing)
- [Dependencies](#dependencies)
- [License](#license)
- [Additional Resources](#additional-resources)

---

## Repository Overview

- ✅ **Automated Azure Infrastructure**: Creates storage accounts, resource groups, service principals with proper RBAC
- ✅ **Kubernetes Integration**: Deploys CSI snapshot classes for Azure disk and NFS volumes
- ✅ **Velero Management**: Automatic installation, configuration, and verification
- ✅ **Cross-Cluster Restore**: Complete restore workflow with CSI driver patches and priority class configuration
- ✅ **Independent Health Monitoring**: Comprehensive SAS Viya health checks with detailed reporting
- ✅ **Configuration Management**: External configuration via `environment.properties` file
- ✅ **State Persistence**: Resumable operations with JSON-based state tracking
- ✅ **Comprehensive Validation**: Pre-flight checks and environment validation

---

## System Requirements

### Workstation Requirements

> Run this tool from a **Linux workstation** that has direct network access to your AKS cluster.

| Category | Details | Minimum | Recommended |
|----------|---------|---------|-------------|
| **OS** | Rocky Linux 9.x, RHEL 9.x, CentOS Stream 9 | — | Rocky Linux 9.x or RHEL 9.x |
| **CPU** | Cores | 2 cores | 4 cores |
| **RAM** | Memory | 4 GB | 8 GB |
| **Disk** | Free space | 10 GB | 20 GB |
| **Network** | Connectivity | Access to AKS API server | Low-latency connection to Azure |

### Required Tools

| Tool | Minimum Version | Installation |
|------|----------------|--------------|
| Go | 1.19+ | [Download Go](https://golang.org/dl/) |
| Azure CLI | 2.0+ | [Install Azure CLI](https://docs.microsoft.com/en-us/cli/azure/install-azure-cli) |
| kubectl | 1.24+ | [Install kubectl](https://kubernetes.io/docs/tasks/tools/) |
| Velero CLI | 1.17+ | [Install Velero](https://velero.io/docs/main/basic-install/) |

### Supported Versions

| Component | Tested / Supported Version | Notes |
|-----------|---------------------------|-------|
| Kubernetes | 1.24+ | AKS cluster version |
| AKS | Latest stable | Azure Kubernetes Service |
| SAS Viya 4 | Tested on and above Stable 2025.09 | Non-Multi-Tenant (NMT) only |
| Velero | 1.17+ | Installed by automation |
| Velero Azure Plugin | 1.10+ | Installed by automation |

### Azure Subscription Access

| Requirement | Level | Purpose |
|-------------|-------|---------|
| **Azure Subscription Role** | Owner | Required to assign RBAC roles, create resource groups, storage accounts, and service principals |
| **Azure AD Role** | Application Administrator or Global Administrator | Required to register and manage service principals used by Velero |

> To verify your current subscription role:
> ```bash
> az role assignment list --assignee $(az ad signed-in-user show --query id -o tsv) \
>   --scope /subscriptions/<SUBSCRIPTION_ID> \
>   --query "[].{Role:roleDefinitionName}" -o table
> # Should show: Owner
> ```

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

> For alternative installation methods (manual Go tarball, older OS versions), refer to the official docs linked in the Required Tools table above.

---

## Quick Start

### 1. Clone and Build

```bash
git clone <repository-url>
cd viya-disaster-recovery

# Build main Velero automation CLI
go build -o azure-viya4-dr

# Build independent health checker
go build -tags health -o viya4-healthcheck viya_health_minimal.go

# Make both executable and verify
chmod +x azure-viya4-dr viya4-healthcheck
./azure-viya4-dr --help
./viya4-healthcheck --help
```

### Available CLI Tools After Build

| Tool | Purpose | Binary | Usage |
|------|---------|--------|-------|
| **Velero Automation CLI** | Complete disaster recovery automation | `azure-viya4-dr` | Backup, restore, infrastructure setup |
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

---

## Configuration

### Set Up `environment.properties`

> ⚠️ **Important**: Before running the automation tool, copy the example file and update **all** variables to match your environment. The default values are placeholders and **must** be replaced with your actual Azure subscription details, cluster names, resource group names, and kubeconfig paths.

```bash
cp environment.properties.example environment.properties
nano environment.properties
```

### Kubeconfig Requirements

> ⚠️ A valid kubeconfig file must be provided for backup or restore operations.

- The automation assumes that the provided kubeconfig contains configuration for a **single target Kubernetes cluster only**.
- Kubeconfig files containing multiple cluster contexts (multiple clusters, users, or contexts) are **not supported**.
- If the kubeconfig contains multiple contexts, the automation may operate against an unintended cluster, leading to unpredictable results.


### Complete Configuration Reference

> ⚠️ **Storage Account Name must be globally unique in Azure**: `VELERO_STORE_NAME` is used to create an Azure Storage Account, which requires a **globally unique name** across all of Azure (3–24 lowercase alphanumeric characters, no hyphens or underscores). The default value `psvelerostore` may already be taken. Use a name specific to your organisation, e.g. `myorgvelerostore` or `acmedr2026store`.

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
| **Restore** | `restore` | Target/DR cluster path | See [Restore Pre-Requisites](#3-restore-operations) |

---

## Operations

### 1. Setup Operations

#### Option 1: Individual Steps (Recommended)

```bash
# Step 1 — Provision Azure resources
# Creates: storage account, resource groups, service principal, RBAC assignments
./azure-viya4-dr --steps=azure

# Step 2 — Configure Kubernetes
# Applies: CSI snapshot classes for Azure disk and NFS volumes
./azure-viya4-dr --steps=kubernetes

# Step 3 — Install and configure Velero
# Installs: Velero with Azure plugin using the service principal from Step 1
./azure-viya4-dr --steps=velero
```

> Use individual steps to run each phase separately with full visibility and the ability to re-run a single step if a failure occurs.

#### Option 2: Full Setup (All steps at once)

```bash
./azure-viya4-dr --steps=azure,kubernetes,velero
```

> Use this when you are confident in the configuration and want to complete the entire setup without interruption.

---

### 2. Backup Operations

> ⚠️ **Important – Generated Credentials**: During backup execution, the automation dynamically generates cloud-specific credentials required to access the backup storage. These credentials are created locally under the `credentials/` directory in the same path from which the backup command is executed.
>
> **Preserve the `credentials/` directory and `state.json` after backup completion.** They are required for any subsequent restore operation using the same backup.

#### Execute Backup

```bash
./azure-viya4-dr --backup

# With debug logging
./azure-viya4-dr --backup --debug
```

#### IAC/DAC Deployments — Backup Order

> If your AKS cluster was provisioned using **IAC (Infrastructure as Code)** and SAS Viya was deployed using **DAC (Deployment as Code)**, you **must** take **two separate backups in the following order**. The `sasoperator` namespace must be backed up first.

**Step 1** — Backup the `sasoperator` namespace:

```bash
./azure-viya4-dr --backup
# When prompted for namespace, select: sasoperator (or sas-deployment-operator)
# Wait for the backup to complete before proceeding
```

**Step 2** — Backup the Viya namespace:

```bash
./azure-viya4-dr --backup
# When prompted for namespace, select: your Viya namespace (e.g. viya4)
# Wait for the backup to complete before proceeding
```

**Step 3** — Verify both backups completed successfully:

```bash
velero backup get
```

Expected output:
```
NAME                 STATUS      ERRORS   WARNINGS   CREATED                         EXPIRES   ...   NAMESPACES
sasoperator-backup   Completed   0        0          2026-03-27 10:00:00 +0000 UTC   29d       ...   sasoperator
viya-backup          Completed   0        0          2026-03-27 10:05:00 +0000 UTC   29d       ...   <VIYA_NAMESPACE>
```

> ✅ Both backups must show **Completed** status with **0 errors** before proceeding to a restore.

---

### 3. Restore Operations

#### Target Cluster Pre-Requisites

> ⚠️ **This tooling does not restore a running SAS Viya environment into another running SAS Viya environment.** The Velero restore process creates the Viya namespace and all of its contents from scratch on the DR cluster. There must be **no existing SAS Viya deployment** in the target namespace before the restore is run.

> 🔒 **The DR/target AKS cluster must reside in the same Azure subscription as the source cluster.** Cross-subscription restore has not been tested. The automation uses a single subscription ID for all Azure resource operations.

Before running any restore command, the DR/target AKS cluster must already have the following **cluster-level infrastructure** in place. These components are *not* restored by Velero and must be provisioned independently on the DR cluster.

| Component | Namespace | Why it is required |
|-----------|-----------|--------------------|
| **Ingress controller** (nginx or Contour/Envoy) | `ingress-nginx` or `projectcontour` | Velero restores Viya `Ingress`/`HTTPProxy` objects — the controller must exist to serve them |
| **NFS CSI driver** | `kube-system` | Required to provision and mount NFS-backed `PersistentVolumes` restored by Velero |
| **Azure Disk CSI driver** | `kube-system` | Required to provision and mount Azure Disk `PersistentVolumes` restored by Velero |
| **NFS StorageClass** | cluster-scoped | Viya PVCs reference a named storage class — it must exist with the same name as on the source cluster |
| **Azure Disk StorageClass** | cluster-scoped | Same requirement as above for disk-based PVCs |
| **cert-manager** (if TLS is used) | `cert-manager` | Certificate `Issuer`/`ClusterIssuer` resources restored by Velero require cert-manager to be running |
| **SAS Deployment Operator** (`sasoperator`) | `sasoperator` | Required for Viya CRD reconciliation — must be restored or pre-installed before the Viya namespace restore |

Verify prerequisites are in place:

```bash
kubectl get pods -n ingress-nginx                              # nginx ingress
kubectl get pods -n projectcontour                             # Contour ingress
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

---

#### Pre-Restore Requirements — Read Before Running

Ensure **all** of the following requirements are met before executing any restore command.

**Requirement 1 — Run from the same working directory as backup**

> 🗂️ The restore command must be executed from the **exact same working directory** in which the backup was originally run.

During backup, the automation creates a `credentials/` subdirectory containing Azure service principal credentials. The restore operation uses these same credentials.

```
<working-directory>/
├── azure-viya4-dr
├── environment.properties
├── credentials/
│   └── credentials-velero.json    ← generated during backup, required for restore
└── state.json
```

**Requirements 2–4 — Update `environment.properties` before running on the DR cluster**

- Set `CLUSTER_TYPE=restore` (was `source` on the backup cluster)
- Set `KUBECONFIG_PATH` to the **DR cluster** admin kubeconfig — not the source cluster
- Confirm the DR cluster is in the **same Azure subscription** as the source cluster (`az account show --query id -o tsv`)


> ⚠️ Additionally, `VELERO_STORE_NAME` and `VELERO_BLOB_RG` **must exactly match** the values used during backup on the source cluster.

**Sample `environment.properties` for Restore**

Copy your backup-time `environment.properties` and apply the changes marked `*** CHANGE FOR RESTORE ***`:

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

---

#### Execute Restore

```bash
# Run restore on DR cluster (from the same directory backup was run)
./azure-viya4-dr --restore

# With debug logging
./azure-viya4-dr --restore --debug
```

---

#### IAC/DAC Deployments — Restore Order

> If your DR cluster was provisioned using **IAC** and SAS Viya was originally deployed using **DAC**, follow these steps **before and during restore**. Skipping this order may cause CRD or operator dependency failures.

**Step 1** — Apply DAC baseline on the DR cluster first:

> See [Target Cluster Pre-Requisites](#target-cluster-pre-requisites) above. The baseline must be fully complete before proceeding.

**Step 2** — Restore the `sasoperator` namespace first:

```bash
./azure-viya4-dr --restore
# When prompted, select: sasoperator-backup (or deployment operator backup)
# Wait for the restore to complete, then validate:
kubectl get pods -n sasoperator
```

**Step 3** — Then restore the Viya namespace:

```bash
./azure-viya4-dr --restore
# When prompted, select: viya-backup (or your Viya namespace backup name)
```

**Step 4** — Verify both restores completed successfully:

```bash
velero restore get
```

Expected output:
```
NAME                    STATUS      ERRORS   WARNINGS   BACKUP
sasoperator-restore     Completed   0        0          sasoperator-backup
viya-restore            Completed   0        0          viya-backup
```

> ✅ Both restores must show **Completed** status with **0 errors** before proceeding.

---

### 4. Post-Restore: Update Ingress for DR Cluster

After a Velero restore, all ingress resources in the Viya namespace still carry the **source cluster's hostnames**. How you handle this depends on your DR scenario.

#### Choose Your Approach

> ⚠️ Do **not** mix the two approaches. Patching ingress hosts **and** switching DNS at the same time will result in a broken configuration.

---

#### Option A — Update Ingress Hosts (Parallel / New URL)

Use this when you want the DR environment accessible via a **different hostname** from the source cluster.

The automation handles ingress patching automatically during restore — it auto-detects the DR cluster's nginx load balancer hostname and patches all Viya ingress rules. No manual intervention is required when running `./azure-viya4-dr --restore`.

If you need to run the patch manually, use the provided helper script:

```bash
NAMESPACE=<VIYA_NAMESPACE> ./scripts/update-ingress.sh
```

Refer to [scripts/update-ingress.sh](scripts/update-ingress.sh) for the full implementation.

**Verify the update:**

```bash
kubectl get ingress -n <VIYA_NAMESPACE>
kubectl describe ingress -n <VIYA_NAMESPACE>
```

---

#### Option B — Switch DNS Alias (True DR Failover)

Use this when the source environment is **broken or being decommissioned** and you want users to continue using the **same URL** routed to the DR cluster. The ingress resources are left unchanged — only DNS is updated.

1. Get the DR cluster's ingress controller load balancer address:

```bash
# nginx
kubectl get svc -n ingress-nginx \
  -o jsonpath='{.items[0].status.loadBalancer.ingress[0].hostname}'

# Contour
kubectl get svc -n projectcontour \
  -o jsonpath='{.items[0].status.loadBalancer.ingress[0].hostname}'
```

2. In your DNS provider (Azure DNS, Route 53, internal DNS, etc.), update the existing DNS record to point to the DR cluster's load balancer address:
   - **CNAME record** (hostname exposed): update the CNAME target
   - **A record** (IP exposed): update the A record value

3. Verify DNS propagation:

```bash
nslookup viya4.mydomain.com
# or
dig viya4.mydomain.com
```

> ✅ Once DNS propagates, users are routed to the DR cluster with no URL change required. No ingress patching is needed.

---

## Cleanup Operations

The `--cleanup` flag provides a **safe, ordered uninstall** of everything the automation created — across Azure, Kubernetes, and local files. Use it when:
- A setup run failed partially and you need to retry from a clean state
- You want to decommission the entire DR setup
- You need to rename resources (e.g. fix a storage account name conflict) and start fresh

> ⚠️ Cleanup is **destructive and irreversible**. It will delete Azure resource groups, the storage account (including all Velero backups stored in it), the service principal, and all Kubernetes resources. You will be prompted to confirm before anything is deleted.

### When to Use Cleanup

| Scenario | Action |
|----------|--------|
| Setup failed mid-way and you want a clean retry | Run `--cleanup`, then re-run setup |
| Storage account name conflict (globally taken) | Run `--cleanup`, update `VELERO_STORE_NAME`, retry |
| Config issue caused partial resource creation | Run `--cleanup` to remove partial resources, fix config, retry |
| Decommissioning the DR setup entirely | Run `--cleanup` |

### Run Cleanup

```bash
./azure-viya4-dr --cleanup
```

> ⚠️ You will be prompted to type `yes` to confirm before anything is deleted.

### What Gets Removed

Resources are removed **in this order** to avoid dependency failures:

| Order | Resource | Detail |
|-------|----------|--------|
| 1 | **Velero Installation** | Uninstalls Velero namespace, deployments, daemonsets |
| 2 | **Service Principal** | Deletes the Velero service principal and its RBAC assignments |
| 3 | **Storage Account** | Deletes the storage account and its blob container |
| 4 | **Resource Groups** | Deletes `VELERO_BLOB_RG` and `VELERO_SNAP_RG` and all contained resources |
| 5 | **Kubernetes Resources** | Removes CSI snapshot classes and related Kubernetes objects |
| 6 | **Local Files** | Removes `credentials/credentials-velero.json` and generated config files |


> 💡 **Tip:** If only a specific step failed, you can re-run just that step without a full cleanup:
> ```bash
> ./azure-viya4-dr --steps=azure       # Re-run only Azure resource creation
> ./azure-viya4-dr --steps=kubernetes  # Re-run only Kubernetes config
> ./azure-viya4-dr --steps=velero      # Re-run only Velero installation
> ```

---

## SAS Viya Health Checker (Independent Tool)

The health checker is a standalone tool that provides comprehensive health monitoring for SAS Viya deployments without requiring Velero or backup operations.

### Quick Start

```bash
./viya4-healthcheck          # Run health check (interactive)
./viya4-healthcheck --help   # Show help
```

### Interactive Workflow

The tool prompts for a kubeconfig path and namespace, runs all checks, then presents an interactive Velero menu:

```
┌─ VELERO INTERACTIVE CHECK ───────────────────────────────
│ What would you like to check?
│ 1. Backups
│ 2. Restores
└─────────────────────────────────────────────────────────
Enter your choice (1 for backups, 2 for restores):
```

### Health Check Components

#### 1. SAS Readiness Pod

- **Status Validation**: Pod phase analysis (Running/Failed/Pending)
- **Container Health**: Ready status and restart count monitoring
- **Health Classification**: HEALTHY (no restarts) vs DEGRADED (with restarts)
- **Detailed Reporting**: Pod name, status, ready containers, restart history

#### 2. CAS Services with Auto-Detection

- **Automatic Setup Detection**: Distinguishes between SMP and MPP configurations
- **Component Validation**: Control pods (`sas-cas-control`), Operator pods (`sas-cas-operator`), Server pods (`sas-cas-server`)
- **Multi-Container Support**: Validates readiness across all containers in CAS pods
- **Worker Analysis**: Automatic worker node detection and health validation
- **Status Classification**: HEALTHY/DEGRADED/UNHEALTHY based on component availability

#### 3. Enhanced PostgreSQL/Crunchy Database Analysis

- **Role-Based Cluster Detection**: Uses `postgres-operator.crunchydata.com/role` labels
- **Master/Replica Validation**: Automatic leader detection and replica health monitoring
- **Advanced Database Analytics**: Database size, table inventory, individual table sizes / row count estimates, schema filtering, size prioritization
- **Connectivity Testing**: Database connection validation on primary pods only
- **Multi-File Reporting**: Separate detailed reports for each PostgreSQL cluster

#### 4. Interactive Velero Integration

- **User Selection Interface**: Interactive choice between backup and restore validation
- **Dynamic Resource Discovery**: Real-time enumeration of available backups/restores
- **Detailed Backup Analysis**: Status validation, creation timestamp, user selection, detail export
- **Restore Validation**: Restore history, phase monitoring, comprehensive restore logs
- **Timestamped Reports**: Organized file naming with creation timestamps

### Sample Health Check Results

```
┌─ SUMMARY ────────────────────────────────────────────────────────────────
│ Overall Status: ✓ HEALTHY
│ Health Score:   95.5%
│ Namespace:      viya4
│ Timestamp:      2026-02-26 15:30:22 UTC
│ Execution Time: 2847ms
│
│ Checks Summary:
│   ✓ Passed:   3/4
│   ⚠ Warning:  1/4
│   ✗ Failed:   0/4
└─────────────────────────────────────────────────────────────────────────

┌─ CAS SERVICES CHECK ─────────────────────────────────────────────────────
│ Status:     ✓ HEALTHY
│ Setup Type: MPP
│ Message:    All CAS services healthy (MPP setup with 3 workers)
│
│ Components:
│   Control:    sas-cas-control-6b4c8d7f9-abc123 (Running, Ready: 1/1)
│   Operator:   sas-cas-operator-7c5d9e8f1-def456 (Running, Ready: 1/1)
│   Controller: sas-cas-server-default-controller-0 (Running, Ready: 3/3)
│   Workers:
│     • sas-cas-server-default-worker-0 (Running, Ready: 3/3)
│     • sas-cas-server-default-worker-1 (Running, Ready: 3/3)
│     • sas-cas-server-default-worker-2 (Running, Ready: 3/3)
└─────────────────────────────────────────────────────────────────────────

┌─ POSTGRESQL CHECK ───────────────────────────────────────────────────────
│ Status:         ✓ HEALTHY
│ Clusters Found: 2
│
│   ✓ sas-crunchy-cds-postgres (3/3 pods healthy)
│     Leader:   sas-crunchy-cds-postgres-00-xyz1 (Running, Ready: 2/2, Role: Primary)
│     Databases: SharedServices (847 MB, 324 tables), DataMart (156 MB, 89 tables)
│     Replicas:  1 instance
└─────────────────────────────────────────────────────────────────────────

┌─ VELERO CHECK ──────────────────────────────────────────────────────────
│ Status:        ✓ HEALTHY
│ Backups Found: 3
│ Latest Backup: sas-viya4-backup-20260226-003830 (Completed)
└─────────────────────────────────────────────────────────────────────────
```

### Reporting System

The health checker generates the following report files:

| Report Type | Format | Contents |
|-------------|--------|----------|
| **Main Health Report** | `viya-health-check-{namespace}-{timestamp}.json` | Overall status, health score, execution metrics, component summaries |
| **Database Reports** (per cluster) | `postgres-database-details-{cluster}-{namespace}-{timestamp}.json` | Table sizes, row count estimates, schema organization, multi-database details |
| **Velero Backup Details** | `velero-backup-details-{backup-name}-{timestamp}.txt` | Full `velero backup describe --details` output |
| **Velero Restore Details** | `velero-restore-details-{restore-name}-{timestamp}.txt` | Comprehensive restore logs and status |

### Advanced Use Cases

```bash
# Daily automated health monitoring
0 2 * * * /path/to/viya4-healthcheck >> /var/log/viya-health.log 2>&1

# Weekly detailed analysis with database metrics
0 3 * * 0 /path/to/viya4-healthcheck >> /var/log/viya-weekly.log 2>&1
```

---

## Debugging & Troubleshooting

### Debug Mode

```bash
./azure-viya4-dr --backup --debug
./azure-viya4-dr --restore --debug
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

| Log | Location |
|-----|----------|
| Application Logs | Console output |
| Velero Logs | `kubectl logs -n velero deployment/velero` |
| State File | `state.json` |
| Credentials | `credentials/credentials-velero.json` |

---

## Repository Structure

```
viya-disaster-recovery/
├── main.go                              # Velero automation CLI entry point
├── viya_health_minimal.go               # Independent health checker (build tag: health)
├── environment_details.go               # Configuration management
├── create_azure_resources.go            # Azure resource creation
├── check_kubernetes_connectivity.go     # Kubernetes operations & restore
├── state_store.go                       # State persistence
├── environment.properties.example       # Example configuration (copy to environment.properties)
├── scripts/
│   ├── backup-permissions.sh            # Permission fix for NFS PVCs (backup)
│   ├── restore-permissions.sh           # Permission fix for NFS PVCs (restore)
│   └── update-ingress.sh                # Post-restore ingress host update (nginx)
├── configs/
│   ├── backup/
│   │   ├── backup-template.yaml
│   │   └── README.md
│   ├── restore/
│   │   ├── restore-template.yaml
│   │   ├── velero-priorityclass.yaml
│   │   └── README.md
│   └── snapshot_classes/
│       ├── azure-disk-snapshot-class.yaml
│       └── azure-nfs-snapshot-class.yaml
```

---

## Security Considerations

- **Service Principal**: Auto-generated with minimal required permissions
- **RBAC**: Contributor role on specific resource groups only
- **Credentials**: Stored locally in `credentials/` (add to `.gitignore`)
- **Storage Access**: Secure blob storage access with SAS tokens
- **Network**: Respects existing AKS network policies

---

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit changes (`git commit -m 'Add amazing feature'`)
4. Push to branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

---

## Dependencies

**Core components:**
- [Velero](https://github.com/vmware-tanzu/velero) — Apache License 2.0
- [kubectl (Kubernetes)](https://kubernetes.io/docs/tasks/tools/) — Apache License 2.0

**Other required tools / images:**
- [Azure CLI](https://github.com/Azure/azure-cli) — MIT License
- [Go](https://go.dev/) — BSD-style license
- [BusyBox container image](https://hub.docker.com/_/busybox) — GPLv2

---

## License

This project is licensed under [Apache 2.0 License](https://github.com/sassoftware/viya-disaster-recovery/blob/main/LICENSE).

---

## Additional Resources

- [Velero Documentation](https://velero.io/docs/)
- [Azure Velero Plugin](https://github.com/vmware-tanzu/velero-plugin-for-microsoft-azure)
- [SAS Viya 4 Documentation](https://documentation.sas.com/doc/en/sasadmincdc/default/calsrvpgm/home.htm)
- [AKS Disaster Recovery Best Practices](https://docs.microsoft.com/en-us/azure/aks/operator-best-practices-multi-region)
