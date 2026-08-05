package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	KubeconfigPath             string
	ClusterType                string
	ViyaDeploymentType         string
	ViyaNamespace              string
	NFSStorageClass            string
	NFSSnapshotClass           string
	SnapshotterVersion         string
	VeleroNamespace            string
	VeleroCredentialSecretName string
	VeleroBucket               string
	VeleroProvider             string
	VeleroPlugin               string
	LibrefsEndpoint            string
	LibrefsBucket              string
	LibrefsAccessKey           string
	LibrefsSecretKey           string
	LibrefsInstallMode         string
	LibrefsBinaryPath          string
	LibrefsOptDir              string
	LibrefsDataDir             string
	LibrefsUser                string
	LibrefsServiceName         string
	LibrefsConsoleAddress      string
	LibrefsAPIPort             string
	LibrefsConsolePort         string
	LibrefsRemoteHost          string
	LibrefsRemoteUser          string
	LibrefsRemoteKeyPath       string
	BackupName                 string
	RestoreName                string
	CredentialsDir             string
	CredentialsFile            string
}

func LoadConfig(path string) (*Config, error) {
	values, err := readProperties(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		KubeconfigPath:             get(values, "KUBECONFIG_PATH", ""),
		ClusterType:                get(values, "CLUSTER_TYPE", "source"),
		ViyaDeploymentType:         get(values, "VIYA_DEPLOYMENT_TYPE", "nmt"),
		ViyaNamespace:              get(values, "VIYA_NAMESPACE", "viya"),
		NFSStorageClass:            get(values, "NFS_STORAGE_CLASS", "sas"),
		NFSSnapshotClass:           get(values, "NFS_SNAPSHOT_CLASS", "nfs-snapshot-class"),
		SnapshotterVersion:         get(values, "SNAPSHOTTER_VERSION", "v8.4.0"),
		VeleroNamespace:            get(values, "VELERO_NAMESPACE", "velero"),
		VeleroCredentialSecretName: get(values, "VELERO_CREDENTIAL_SECRET_NAME", ""),
		VeleroBucket:               get(values, "VELERO_BUCKET", "velero"),
		VeleroProvider:             get(values, "VELERO_PROVIDER", "aws"),
		VeleroPlugin:               get(values, "VELERO_PLUGIN", "velero/velero-plugin-for-aws:v1.12.1"),
		LibrefsEndpoint:            get(values, "LIBREFS_ENDPOINT", "http://127.0.0.1:9000"),
		LibrefsBucket:              get(values, "LIBREFS_BUCKET", "velero"),
		LibrefsAccessKey:           get(values, "LIBREFS_ACCESS_KEY", "velero"),
		LibrefsSecretKey:           get(values, "LIBREFS_SECRET_KEY", "velero12345"),
		LibrefsInstallMode:         get(values, "LIBREFS_INSTALL_MODE", "skip"),
		LibrefsBinaryPath:          get(values, "LIBREFS_BINARY_PATH", "./librefs-linux-amd64"),
		LibrefsOptDir:              get(values, "LIBREFS_OPT_DIR", "/opt/librefs"),
		LibrefsDataDir:             get(values, "LIBREFS_DATA_DIR", "/data/librefs"),
		LibrefsUser:                get(values, "LIBREFS_USER", "librefs"),
		LibrefsServiceName:         get(values, "LIBREFS_SERVICE_NAME", "librefs"),
		LibrefsConsoleAddress:      get(values, "LIBREFS_CONSOLE_ADDRESS", ":9001"),
		LibrefsAPIPort:             get(values, "LIBREFS_API_PORT", "9000"),
		LibrefsConsolePort:         get(values, "LIBREFS_CONSOLE_PORT", "9001"),
		LibrefsRemoteHost:          get(values, "LIBREFS_REMOTE_HOST", ""),
		LibrefsRemoteUser:          get(values, "LIBREFS_REMOTE_USER", "rocky"),
		LibrefsRemoteKeyPath:       get(values, "LIBREFS_REMOTE_KEY_PATH", ""),
		BackupName:                 get(values, "BACKUP_NAME", "viya-full-backup"),
		RestoreName:                get(values, "RESTORE_NAME", "viya-restore"),
		CredentialsDir:             get(values, "CREDENTIALS_DIR", "credentials"),
		CredentialsFile:            get(values, "CREDENTIALS_FILE", "velero-creds-local"),
	}
	return cfg, nil
}

func (c *Config) ValidateForSetup() error {
	if c.KubeconfigPath == "" {
		return fmt.Errorf("KUBECONFIG_PATH is required")
	}
	if c.ViyaDeploymentType != "nmt" {
		return fmt.Errorf("only VIYA_DEPLOYMENT_TYPE=nmt is supported for Velero backup/restore")
	}
	if c.ClusterType != "source" && c.ClusterType != "restore" {
		return fmt.Errorf("CLUSTER_TYPE must be source or restore")
	}
	return nil
}

func readProperties(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"")
		values[key] = value
	}
	return values, scanner.Err()
}

func get(values map[string]string, key, fallback string) string {
	if v, ok := values[key]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}
