# Velero AWS Automation for SAS Viya 4 Disaster Recovery

[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org)
[![AWS CLI](https://img.shields.io/badge/AWS%20CLI-2.0+-orange.svg)](https://aws.amazon.com/cli/)
[![kubectl](https://img.shields.io/badge/kubectl-1.24+-blue.svg)](https://kubernetes.io/docs/tasks/tools/)
[![eksctl](https://img.shields.io/badge/eksctl-Latest-blue.svg)](https://eksctl.io/)

> **⚠️ Important:** This is the AWS-specific implementation. All commands in this README should be run from the `aws/` directory.

Comprehensive automation suite for deploying Velero on Amazon EKS clusters, enabling disaster recovery for SAS Viya 4 Non-Multi-Tenant (NMT) deployments. This solution provides a modular Go implementation using AWS SDK v2 and includes two main tools: the core Velero automation CLI and an independent SAS Viya health checker.

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

- ✅ **Automated AWS Infrastructure**: Creates S3 buckets, IAM roles/policies, EBS CSI driver setup
- ✅ **EKS OIDC Integration**: Automatic OIDC provider detection and IRSA configuration
- ✅ **Kubernetes Integration**: Deploys CSI snapshot classes for EBS and NFS volumes
- ✅ **Velero Management**: Automatic installation, configuration, and verification
- ✅ **Cross-Cluster Restore**: Complete restore workflow with proper IAM configuration
- ✅ **Configuration Management**: External configuration via `environment.properties` file
- ✅ **State Persistence**: Resumable operations with JSON-based state tracking
- ✅ **Comprehensive Validation**: Pre-flight checks for AWS credentials, tools, and cluster access
- ✅ **Independent Health Monitoring**: Comprehensive SAS Viya health checks with detailed reporting (standalone or integrated)

## System Requirements

### Workstation Requirements

> Run this tool from a Linux workstation that has direct network access to your EKS cluster.

| Category | Details | Minimum | Recommended |
|-|-|---------|-------------|
| OS | | Rocky Linux 9.x, RHEL 9.x, CentOS Stream 9 | Rocky Linux 9.x or RHEL 9.x |
| CPU | Cores | 2 cores | 4 cores |
| RAM | Memory | 4 GB | 8 GB |
| Disk | Free space | 10 GB | 20 GB |
| Network | Connectivity | Access to EKS API server | Low-latency connection to AWS |

### Required Tools

| Tool | Minimum Version | Installation |
|------|----------------|--------------|
| **Go** | 1.21+ | [Download Go](https://golang.org/dl/) |
| **AWS CLI** | 2.0+ | [Install AWS CLI](https://aws.amazon.com/cli/) |
| **kubectl** | 1.24+ | [Install kubectl](https://kubernetes.io/docs/tasks/tools/) |
| **eksctl** | Latest | [Install eksctl](https://eksctl.io/) |
| **Velero CLI** | 1.17+ | [Install Velero](https://velero.io/docs/main/basic-install/) |

### Supported Versions

| Component | Version | Notes |
|-----------|---------|-------|
| Kubernetes | 1.24+ | EKS cluster version |
| EKS | Latest stable | Amazon Elastic Kubernetes Service |
| SAS Viya 4 | Tested on and above Stable 2025.09 | Non-Multi-Tenant (NMT) only |
| Velero | 1.17+ | Installed by automation |
| Velero AWS Plugin | 1.13+ | Installed by automation |

### AWS Account Access

| Role | Requirement |
|------|-------------|
| AWS IAM | Permissions to create IAM roles, policies, users, and OIDC providers |
| AWS S3 | Permissions to create and manage S3 buckets |
| AWS EKS | `eks:DescribeCluster` to retrieve OIDC provider ID |
| AWS STS | `sts:GetCallerIdentity` for account verification |

```bash
# Verify your current AWS identity
aws sts get-caller-identity
```

### Installation Commands

```bash
# Go — CentOS/RHEL/Rocky Linux
sudo dnf install golang

# AWS CLI v2
curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip"
unzip awscliv2.zip && sudo ./aws/install

# kubectl
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl

# eksctl
ARCH=amd64
PLATFORM=$(uname -s)_$ARCH
curl -sLO "https://github.com/eksctl-io/eksctl/releases/latest/download/eksctl_$PLATFORM.tar.gz"
tar -xzf eksctl_$PLATFORM.tar.gz -C /tmp && rm eksctl_$PLATFORM.tar.gz
sudo mv /tmp/eksctl /usr/local/bin

# Velero CLI
wget https://github.com/vmware-tanzu/velero/releases/download/v1.17.0/velero-v1.17.0-linux-amd64.tar.gz
tar -xzf velero-v1.17.0-linux-amd64.tar.gz
sudo mv velero-v1.17.0-linux-amd64/velero /usr/local/bin/

# Verify all installations
go version && aws --version && kubectl version --client && eksctl version && velero version --client-only
```

## Quick Start

### 1. Clone and Build

```bash
git clone <repository-url>
cd viya-disaster-recovery/aws

# Build the automation tool
go build -o viya-dr-automation-aws .

# Make executable and verify
chmod +x viya-dr-automation-aws
./viya-dr-automation-aws --help
```

### 2. AWS Authentication

> **Disclaimer**: This automation only requires your AWS credentials for resource management. No user credentials are stored in the configuration. It assumes you already have appropriate access to the AWS account being used.

> **IAM Requirements**: We create IAM roles and policies as part of the DR process which will be used by Velero and EBS CSI driver. Make sure you have permissions to create IAM roles, policies, and OIDC providers in the AWS account.

```bash
# Configure AWS credentials
aws configure

# Verify authentication
aws sts get-caller-identity

# Update EKS kubeconfig
aws eks update-kubeconfig --name your-eks-cluster --region us-east-1
```

## Configuration

### Set Up `environment.properties`

> **⚠️ Important:** Before running the automation tool, copy the example file and update all variables to match your environment. The default values are placeholders and must be replaced with your actual AWS account details, cluster names, and kubeconfig paths.

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

```properties
# AWS Configuration
REGION=us-east-1                              # AWS region
ACCOUNT_ID=XXXXXXXXXXX                       # AWS account ID (auto-detected if not set)

# S3 Bucket for Velero Backups
VELERO_BUCKET_NAME=your-velero-backup-bucket  # S3 bucket name (must be globally unique)
VELERO_BUCKET_REGION=us-east-1               # S3 bucket region

# Kubernetes Configuration
KUBECONFIG_PATH=/path/to/admin-kubeconfig           # Cluster kubeconfig path
NFS_STORAGE_CLASS=efs-sc                      # Storage class for EFS/NFS permission backup PVC

# EKS Cluster Configuration
CLUSTER_NAME=your-eks-cluster                 # EKS cluster name
EKS_CLUSTER_REGION=us-east-1                  # EKS cluster region

# Viya Deployment Configuration
VIYA_DEPLOYMENT_TYPE=nmt                      # nmt|mt (only nmt supported)
VIYA_NAMESPACE=viya4                          # Viya namespace to backup/restore
CLUSTER_TYPE=source                           # source|restore

# IAM Configuration
USE_IAM_USER=true                             # Use IAM user (true) or IRSA/role (false)
VELERO_IAM_USER=velero-primary               # Velero IAM user (when USE_IAM_USER=true)
EBS_ROLE_NAME=your-ebs-csi-role              # EBS CSI driver IAM role
```

### Variable Changes by Operation

| Operation | CLUSTER_TYPE | KUBECONFIG_PATH | CLUSTER_NAME |
|-----------|--------------|-----------------|--------------|
| **Setup** | `source` | Source cluster path | Source EKS cluster |
| **Backup** | `source` | Source cluster path | Source EKS cluster |
| **Restore** | `restore` | Target cluster path | Target EKS cluster |

## Operations

### 1. Setup Operations

#### Option 1: Individual Steps (Recommended)

```bash
# Step 1 — Provision AWS resources
# Creates: S3 bucket, IAM roles/policies, IAM user/access keys (if USE_IAM_USER=true)
./viya-dr-automation-aws --steps aws

# Step 2 — Configure Kubernetes
# Installs: EBS CSI driver addon, CSI snapshot controller, VolumeSnapshotClasses
./viya-dr-automation-aws --steps kubernetes

# Step 3 — Install and configure Velero
# Installs: Velero with AWS plugin, node agent (Kopia), CSI integration
./viya-dr-automation-aws --steps velero
```

> Use individual steps to run each phase separately with full visibility and the ability to re-run a single step if a failure occurs.

#### Option 2: Full Setup (All steps at once)

```bash
./viya-dr-automation-aws --steps all
```

> Use this when you are confident in the configuration and want to complete the entire setup without interruption.

### 2. Backup Operations

> **⚠️ Important – Generated Credentials:** During backup execution, access keys for the Velero IAM user (if using `USE_IAM_USER=true`) are stored under `credentials/` in the same working directory. The `state.json` file records the backup name for later restore reference.
>
> Preserve the `credentials/` directory and `state.json` after backup completion. They are required for any subsequent restore operation using the same backup.

#### Execute Backup

```bash
./viya-dr-automation-aws --backup
```

The backup workflow:
1. Validates `CLUSTER_TYPE=source` and `VIYA_DEPLOYMENT_TYPE=nmt`
2. Validates kubeconfig and cluster connectivity
3. Verifies Velero is running and backup location is available
4. **Runs `scripts/backup-permissions.sh`** — snapshots POSIX permissions of all EFS NFS volumes before backup
5. Creates a timestamped Velero backup (e.g., `viya-backup-20260304-153045`)
6. Monitors backup until `Completed` or `Failed` (30-minute timeout)
7. Records backup name in `state.json` for restore reference

**Required environment variables:**
```properties
CLUSTER_TYPE=source                    # MUST be "source"
VIYA_DEPLOYMENT_TYPE=nmt              # MUST be "nmt"
KUBECONFIG_PATH=/path/to/source/kubeconfig
CLUSTER_NAME=source-eks-cluster
VELERO_BUCKET_NAME=velero-backup-bucket
NFS_STORAGE_CLASS=efs-sc              # EFS StorageClass for permission snapshot PVC
```

#### IAC/DAC Deployments — Backup Order

> If your EKS cluster was provisioned using IAC (Infrastructure as Code) and SAS Viya was deployed using DAC (Deployment as Code), you must take two separate backups in the following order. The `sasoperator` namespace must be backed up first.

Step 1 — Backup the `sasoperator` namespace:

```bash
./viya-dr-automation-aws --backup
# When prompted for namespace, enter: sasoperator (or sas-deployment-operator)
# Wait for the backup to complete before proceeding
```

Step 2 — Backup the Viya namespace:

```bash
./viya-dr-automation-aws --backup
# When prompted for namespace, enter: your Viya namespace (e.g. viya4)
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

Before running any restore command, the DR/target EKS cluster must already have the following cluster-level infrastructure in place:

| Component | Namespace | Why Required |
|-----------|-----------|--------------|
| Ingress controller (nginx) | `ingress-nginx` | Velero restores Viya Ingress objects — the controller must exist to serve them |
| EFS CSI driver | `kube-system` | Required to provision and mount EFS NFS-backed PersistentVolumes restored by Velero |
| EBS CSI driver | `kube-system` | Required to provision and mount EBS PersistentVolumes restored by Velero |
| NFS StorageClass (EFS) | cluster-scoped | Viya PVCs reference a named storage class — it must exist with the same name as on the source cluster |
| EBS StorageClass | cluster-scoped | Same requirement as above for EBS-backed PVCs |
| cert-manager (if TLS is used) | `cert-manager` | Certificate Issuer/ClusterIssuer resources restored by Velero require cert-manager to be running |
| SAS Deployment Operator (sasoperator) | `sasoperator` | Required for Viya CRD reconciliation — must be restored or pre-installed before the Viya namespace restore |

```bash
# Verify prerequisites are in place:
kubectl get pods -n ingress-nginx                                              # nginx ingress
kubectl get pods -n kube-system -l app=efs-csi-node                           # EFS CSI driver
kubectl get pods -n kube-system -l app.kubernetes.io/name=aws-ebs-csi-driver  # EBS CSI driver
kubectl get storageclass                                                       # Storage classes (names must match source cluster)
kubectl get pods -n cert-manager                                               # cert-manager (if applicable)
kubectl get pods -n sasoperator                                                # SAS Deployment Operator
```

> ✅ All required components must show **Running** pods before you proceed. The restore will fail silently or leave PVCs in `Pending` state if the CSI drivers or storage classes are missing.

#### Pre-Restore Requirements — Read Before Running

**Requirement 1 — Run from the same working directory as backup**

> 🗂️ The restore command must be executed from the **exact same working directory** in which the backup was originally run.

During backup, the automation records the backup name in `state.json` and stores credentials under `credentials/`. The restore operation uses these.

```
<working-directory>/
├── viya-dr-automation-aws
├── environment.properties
├── credentials/
│   └── aws-velero-credentials    ← generated during setup, required for restore
└── state.json
```

**Requirements 2–3 — Update `environment.properties` before running on the DR cluster**

- Set `CLUSTER_TYPE=restore` (was `source` on the backup cluster)
- Set `KUBECONFIG_PATH` to the DR cluster admin kubeconfig — not the source cluster

> **⚠️** Additionally, `VELERO_BUCKET_NAME` and `VELERO_BUCKET_REGION` must **exactly match** the values used during backup on the source cluster.

Sample `environment.properties` for Restore:

```properties
# =======================================================
# environment.properties — DR Cluster (Restore)
# =======================================================

# AWS S3 — MUST match backup cluster values exactly
VELERO_BUCKET_NAME=your-velero-backup-bucket
VELERO_BUCKET_REGION=us-east-1

# AWS region — MUST match backup cluster value
REGION=us-east-1
ACCOUNT_ID=123456789012

# *** CHANGE FOR RESTORE *** — must be "restore" (was "source" on backup cluster)
CLUSTER_TYPE=restore

# Only "nmt" supported
VIYA_DEPLOYMENT_TYPE=nmt

# *** CHANGE FOR RESTORE *** — must point to DR cluster kubeconfig (not source cluster)
KUBECONFIG_PATH=/path/to/dr-cluster/admin-kubeconfig

# *** CHANGE FOR RESTORE *** — must be the DR EKS cluster name
CLUSTER_NAME=your-dr-eks-cluster
EKS_CLUSTER_REGION=us-east-1

# Storage class for EFS permission backup PVC
NFS_STORAGE_CLASS=efs-sc

# IAM Configuration — MUST match backup cluster values
USE_IAM_USER=true
VELERO_IAM_USER=velero-primary
```

#### Execute Restore

```bash
# Run restore on DR cluster (from the same directory backup was run)
./viya-dr-automation-aws --restore
```

The restore workflow:
1. Validates `CLUSTER_TYPE=restore` and `VIYA_DEPLOYMENT_TYPE=nmt`
2. Validates kubeconfig and DR cluster connectivity
3. Configures S3 bucket access and IAM
4. Installs CSI snapshot classes (EBS and NFS) on the DR cluster
5. Installs Velero with AWS configuration pointing to same S3 bucket
6. **Lists available backups** from S3 and prompts you to select one
7. Prompts for the namespace to restore into
8. Executes Velero restore and monitors until completion
9. **Step 10 — Runs `scripts/restore-permissions.sh`** — restores POSIX permissions on all EFS NFS volumes
10. **Step 11 — Runs `scripts/update-ingress.sh`** — patches all Viya ingress hosts to the DR cluster's NLB hostname
11. **Step 12 — Restore complete**

#### IAC/DAC Deployments — Restore Order

> If your DR cluster was provisioned using IAC and SAS Viya was originally deployed using DAC, follow these steps before and during restore.

Step 1 — Apply DAC baseline on the DR cluster first:

> See [Target Cluster Pre-Requisites](#target-cluster-pre-requisites) above. The baseline must be fully complete before proceeding.

Step 2 — Restore the `sasoperator` namespace first:

```bash
./viya-dr-automation-aws --restore
# When prompted, select: sasoperator-backup (or deployment operator backup name)
# Wait for the restore to complete, then validate:
kubectl get pods -n sasoperator
```

Step 3 — Then restore the Viya namespace:

```bash
./viya-dr-automation-aws --restore
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

The `--cleanup` flag provides a safe, ordered uninstall of everything the automation created — across AWS, Kubernetes, and local files. Use it when:

- A setup run failed partially and you need to retry from a clean state
- You want to decommission the entire DR setup
- You need to rename resources (e.g. fix an S3 bucket name conflict) and start fresh

> **⚠️** Cleanup is destructive and irreversible. It will delete IAM resources, Velero installation, and Kubernetes objects. You will be prompted to confirm before anything is deleted.

### When to Use Cleanup

| Scenario | Action |
|----------|--------|
| Setup failed mid-way and you want a clean retry | Run `--cleanup`, then re-run setup |
| S3 bucket name conflict | Run `--cleanup`, update `VELERO_BUCKET_NAME`, retry |
| Config issue caused partial resource creation | Run `--cleanup` to remove partial resources, fix config, retry |
| Decommissioning the DR setup entirely | Run `--cleanup` |

### Run Cleanup

```bash
./viya-dr-automation-aws --cleanup
```

> **⚠️** You will be prompted to confirm before anything is deleted.

### What Gets Removed

| Order | Resource | Description |
|-------|----------|-------------|
| 1 | Velero Installation | Uninstalls Velero namespace, deployments, daemonsets |
| 2 | VolumeSnapshotClasses | Removes EBS and NFS snapshot classes |
| 3 | CSI Snapshot Controller | Removes snapshot controller from kube-system |
| 4 | EBS CSI Addon | Removes the EBS CSI driver EKS addon |
| 5 | IAM Resources | Deletes roles, policies, users, and access keys |
| 6 | S3 Bucket | Optionally removes the Velero backup bucket |


## SAS Viya Health Checker (Independent Tool)

The health checker is a standalone tool that provides comprehensive health monitoring for SAS Viya deployments without requiring Velero or backup operations.
Please refer to [`SAS Viya Health Checker`](https://github.com/sassoftware/viya-disaster-recovery/blob/aws-support/README.md#-sas-viya-health-checker-standalone)


## Debugging & Troubleshooting

### Common Issues

1. **AWS Authentication Failures**
   ```bash
   # Re-authenticate with AWS
   aws configure

   # Verify credentials
   aws sts get-caller-identity

   # Check IAM permissions
   aws iam get-user --user-name velero-primary
   ```

2. **Kubeconfig Issues**
   ```bash
   # Update kubeconfig
   aws eks update-kubeconfig --name your-cluster --region us-east-1

   # Verify cluster access
   kubectl cluster-info

   # Update path in environment.properties
   KUBECONFIG_PATH=/correct/path/to/kubeconfig
   ```

3. **EBS CSI Driver Addon Failures**
   ```bash
   # Check addon status
   eksctl get addon --cluster your-cluster --name aws-ebs-csi-driver

   # Check pods after installation
   kubectl get pods -n kube-system -l app.kubernetes.io/name=aws-ebs-csi-driver

   # Check driver logs
   kubectl logs -n kube-system -l app=ebs-csi-controller
   ```

4. **Velero Installation Issues**
   ```bash
   # Check Velero pods
   kubectl get pods -n velero

   # Check Velero logs
   kubectl logs -n velero deployment/velero

   # Verify backup location
   velero backup-location get

   # Check credentials (if using IAM user)
   kubectl get secret -n velero cloud-credentials -o yaml
   ```

5. **Access Key Limit Exceeded**

   If you see: `Cannot exceed quota for AccessKeysPerUser: 2`

   ```bash
   # List existing keys
   aws iam list-access-keys --user-name velero-primary

   # Delete old key
   aws iam delete-access-key --user-name velero-primary --access-key-id AKIA...

   # Re-run automation
   ./aws-viya4-dr --steps aws
   ```

6. **Permission Script Failures**
   ```bash
   # Check script is executable
   ls -la scripts/

   # Run manually to debug
   NAMESPACE=viya4 STORAGE_CLASS=efs-sc KUBECONFIG=/path/to/kubeconfig \
     bash scripts/backup-permissions.sh

   # Check EFS CSI driver is running
   kubectl get pods -n kube-system -l app=efs-csi-node
   ```

### State Management

```bash
# Check operation state
cat state.json

# Reset state (forces fresh start)
rm state.json
```

### Log Locations

| Source | Location |
|--------|----------|
| Application Logs | Console output |
| Velero Logs | `kubectl logs -n velero deployment/velero` |
| EBS CSI Logs | `kubectl logs -n kube-system -l app=ebs-csi-controller` |
| State File | `state.json` |

