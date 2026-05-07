# SAS Viya 4 Disaster Recovery Automation

[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org)
[![kubectl](https://img.shields.io/badge/kubectl-1.24+-blue.svg)](https://kubernetes.io/docs/tasks/tools/)
[![Velero](https://img.shields.io/badge/Velero-1.17+-green.svg)](https://velero.io)

Multi-cloud disaster recovery automation suite for SAS Viya 4 Non-Multi-Tenant (NMT) deployments using Velero. This repository provides cloud-specific implementations for fully automated backup, restore, and post-restore validation workflows.

## Requirements

| Tool | Version | Notes |
|------|---------|-------|
| Go | 1.21+ | Build automation + health checker |
| kubectl | 1.24+ | Cluster interaction |
| Velero CLI | 1.17+ | Installed by automation |
| Azure CLI | 2.0+ | Azure implementation only |
| AWS CLI | 2.0+ | AWS implementation only |
| eksctl | Latest | AWS implementation only |
| python3 + PyYAML | 3.6+ | Required by `update-ingress.sh` rollback |

---

## Available Implementations

### ✅ Azure (AKS)

Complete Velero DR automation for Azure Kubernetes Service.

**Location:** [`azure/`](azure/) — see [azure/README.md](azure/README.md)

| Feature | Status |
|---------|--------|
| Azure infrastructure setup (storage account, service principal, RBAC) | ✅ |
| CSI snapshot classes for Azure Disk + NFS volumes | ✅ |
| Velero install + node-agent with SAS tolerations | ✅ |
| Cross-cluster restore with priority class | ✅ |
| NFS permission preservation (backup + restore) | ✅ |
| Post-restore ingress host + TLS patching | ✅ |
| SAS Viya health monitoring | ✅ |

```bash
cd azure
go build -o viya-dr-automation-azure .
./viya-dr-automation-azure --help

# Backup
./viya-dr-automation-azure --backup

# Restore
./viya-dr-automation-azure --restore

# Health checker (integrated)
go build -tags health -o viya-health-checker .
./viya-health-checker
```
---

### ✅ AWS (EKS)

Complete Velero DR automation for Amazon Elastic Kubernetes Service.

**Location:** [`aws/`](aws/) — see [aws/README.md](aws/README.md)

| Feature | Status |
|---------|--------|
| AWS infrastructure setup (S3 bucket, IAM roles/policies, IRSA) | ✅ |
| CSI snapshot classes for EBS + NFS volumes | ✅ |
| Velero install + node-agent with SAS tolerations | ✅ |
| Cross-cluster restore with IAM credential reuse | ✅ |
| EFS NFS permission preservation (backup + restore) | ✅ |
| Post-restore ingress host + TLS patching | ✅ |
| SAS Viya health monitoring | ✅ |

```bash
cd aws
go build -o viya-dr-automation-aws .
./viya-dr-automation-aws --help

# Backup
./viya-dr-automation-aws --backup

# Restore
./viya-dr-automation-aws --restore

# Health checker (integrated)
go build -tags health -o viya-health-checker .
./viya-health-checker
```

---

### ✅ SAS Viya Health Checker (Standalone)

An independent health monitoring tool that works with **any** Kubernetes cluster — Azure, AWS, or on-premises. No DR workflow required.

**Location:** [`health-checker/`](health-checker/)

#### Quick Start

```bash
# Build standalone (no cloud SDK, no build tags)
cd health-checker
go build -o viya-health-checker .
./viya-health-checker

# Or use integrated build inside aws/ or azure/
cd aws   # or azure/
go build -tags health -o viya-health-checker .
./viya-health-checker
```

#### Interactive Workflow

The tool prompts for a kubeconfig path and namespace, runs all checks, then presents an interactive Velero menu:

```
┌─ VELERO INTERACTIVE CHECK ───────────────────────────────
│ What would you like to check?
│ 1. Backups
│ 2. Restores
└─────────────────────────────────────────────────────────
Enter your choice (1 for backups, 2 for restores):
```

#### Health Check Components

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

#### Sample Health Check Results
```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                        SAS VIYA HEALTH CHECKER                                  │
│                                  v1.0.0                                         │
└─────────────────────────────────────────────────────────────────────────────────┘
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
                            HEALTH CHECK RESULTS
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
┌─ SUMMARY ────────────────────────────────────────────────────────────────────
│  Overall Status: ✓ HEALTHY
│  Health Score:  100.0%
│  Namespace:     viya4-dr-test
│  Timestamp:     2026-05-06 02:02:26 EDT
│  Execution Time: 289268ms
│
│  Checks Summary:
│   ✓ Passed:   4/4
│   ⚠ Warning:  0/4
│   ✗ Failed:   0/4
└───────────────────────────────────────────────────────────────────────────────
┌─ READINESS CHECK ─────────────────────────────────────────────────────────────
│  Status: ✓ HEALTHY
│  Pod:    sas-readiness-699c94bcd-5hmlw
│  Ready:  true
│  Phase:  Running
│  Restarts: 0
│  Message: Pod running with no restarts
└───────────────────────────────────────────────────────────────────────────────
┌─ CAS SERVICES CHECK ─────────────────────────────────────────────────────────
│  Status:     ✓ HEALTHY
│  Setup Type: SMP
│  Message:    All CAS services healthy (SMP setup with 0 workers)
│
│  Components:
│    Control:    sas-cas-control-6bd7489f6c-4kq9g (Running, Ready: 1/1)
│    Operator:   sas-cas-operator-b754dbd5c-nn2r5 (Running, Ready: 1/1)
│    Controller: sas-cas-server-default-controller (Running, Ready: 3/3)
└───────────────────────────────────────────────────────────────────────────────
┌─ POSTGRESQL CHECK ───────────────────────────────────────────────────────────
│  Status:       ✓ HEALTHY
│  Clusters Found: 1
│  Message:      All 1 PostgreSQL cluster(s) healthy
│
│  Cluster Details:
│   ✓ sas-crunchy-platform-postgres (4/4 pods healthy)
│     Leader:   sas-crunchy-platform-postgres-00-6t28-0 (Running, Ready: 5/5, Role: Primary)
│     Connect:  ✓ PostgreSQL connectivity verified on leader: sas-crunchy-platform-postgres-00-6t28-0
│     Databases (2 total):
│       • SharedServices: 363 MB, 803 tables
│       • postgres: 7919 kB, 47 tables
│       Total tables across all databases: 850
│     Repo:     sas-crunchy-platform-postgres-repo-host-0 (Running, Ready: 2/2)
│     Replicas: 2 instances
│       • sas-crunchy-platform-postgres-00-9p44-0 (Running, Ready: 5/5)
│       • sas-crunchy-platform-postgres-00-xtkq-0 (Running, Ready: 5/5)
│
│     Status:   All pods healthy - master: sas-crunchy-platform-postgres-00-6t28-0, replicas: 2
│
└───────────────────────────────────────────────────────────────────────────────
┌─ VELERO CHECK ──────────────────────────────────────────────────────────────
│  Status:        ✓ HEALTHY
│  Installation:  true
│  Backups Found: 1
│  Message:       Selected restore 'restore-sas-viya4-backup-20260505-042533-20260506-012236' is healthy
│
│  Latest Backup:
│   ✓ restore-sas-viya4-backup-20260505-042533-20260506-012236
│     Created:          2026-05-06 05:22:36
│     Detail File:      velero-restore-details-restore-sas-viya4-backup-20260505-042533-20260506-012236-20260506-020221.txt
│     Status:           HEALTHY
│
└───────────────────────────────────────────────────────────────────────────────
┌─ RECOMMENDATIONS ────────────────────────────────────────────────────────────
│ 1. System appears healthy - continue regular monitoring
└───────────────────────────────────────────────────────────────────────────────
✓ Report automatically saved: viya-health-check-viya4-dr-test-20260506-020226.json
```

#### Reporting System

The health checker generates the following report files:

| Report | Filename Pattern | Contents |
|--------|-----------------|----------|
| Main Health Report | `viya-health-check-{namespace}-{timestamp}.json` | Overall status, health score, execution metrics, component summaries |
| Database Reports (per cluster) | `postgres-database-details-{cluster}-{namespace}-{timestamp}.json` | Table sizes, row count estimates, schema organization, multi-database details |
| Velero Backup Details | `velero-backup-details-{backup-name}-{timestamp}.txt` | Full `velero backup describe --details` output |
| Velero Restore Details | `velero-restore-details-{restore-name}-{timestamp}.txt` | Comprehensive restore logs and status |

---

## Common Workflow

Regardless of cloud provider, the DR workflow follows the same phases:

```
SOURCE CLUSTER                        RESTORE CLUSTER
──────────────────────────────────    ──────────────────────────────────
1. Setup infra + install Velero       1. Setup infra + install Velero
2. Run --backup                       2. Run --restore
3. scripts/backup-permissions.sh      3. scripts/restore-permissions.sh (auto)
                                      4. scripts/update-ingress.sh (manual)
```
---

## Security Considerations

- **Service Principal / IAM**: Auto-generated with minimal required permissions
- **RBAC**: Contributor role on specific resource groups only (Azure); scoped IAM roles (AWS)
- **Credentials**: Stored locally in `credentials/`
- **Storage Access**: Secure blob storage with SAS tokens (Azure); S3 with encryption and versioning (AWS)
- **Network**: Respects existing cluster network policies and security groups

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

- [Azure CLI](https://docs.microsoft.com/en-us/cli/azure/) — MIT License
- [AWS CLI](https://aws.amazon.com/cli/) — MIT License
- [eksctl](https://eksctl.io/) — Apache License 2.0
- [Go](https://go.dev/) — BSD-style license
- [BusyBox container image](https://hub.docker.com/_/busybox) — GPLv2

---

## License

This project is licensed under [Apache 2.0 License](LICENSE).

---

## Additional Resources

- [Velero Documentation](https://velero.io/docs/)
- [Azure Velero Plugin](https://github.com/vmware-tanzu/velero-plugin-for-microsoft-azure)
- [Velero AWS Plugin](https://github.com/vmware-tanzu/velero-plugin-for-aws)
- [SAS Viya 4 Documentation](https://documentation.sas.com/doc/en/sasadmincdc/default/calsrvpgm/home.htm)
- [AKS Disaster Recovery Best Practices](https://learn.microsoft.com/en-us/azure/aks/operator-best-practices-multi-region)
- [AWS EKS Best Practices](https://aws.github.io/aws-eks-best-practices/)
