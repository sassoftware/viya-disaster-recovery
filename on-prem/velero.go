package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func setupVelero(cfg *Config, r Runner) error {
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}
	credPath, err := writeVeleroCredentials(cfg)
	if err != nil {
		return err
	}
	args := []string{
		"install",
		"--features=EnableCSI",
		"--use-node-agent",
		"--default-snapshot-move-data",
		"--provider", cfg.VeleroProvider,
		"--plugins", cfg.VeleroPlugin,
		"--bucket", cfg.LibrefsBucket,
		"--secret-file", credPath,
		"--use-volume-snapshots=true",
		"--backup-location-config", fmt.Sprintf("region=minio,s3ForcePathStyle=true,s3Url=%s", cfg.LibrefsEndpoint),
		"--namespace", cfg.VeleroNamespace,
	}
	if err := r.Run("velero", args...); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			fmt.Println("Velero appears to be already installed; continuing")
		} else {
			return err
		}
	}
	return r.Run("velero", "backup-location", "get", "--namespace", cfg.VeleroNamespace)
}

func writeVeleroCredentials(cfg *Config) (string, error) {
	if err := os.MkdirAll(cfg.CredentialsDir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(cfg.CredentialsDir, cfg.CredentialsFile)
	content := fmt.Sprintf(`[default]
aws_access_key_id=%s
aws_secret_access_key=%s
`, cfg.LibrefsAccessKey, cfg.LibrefsSecretKey)
	return path, os.WriteFile(path, []byte(content), 0600)
}

func createBackup(cfg *Config, r Runner) error {
	if cfg.ClusterType != "source" {
		return fmt.Errorf("backup requires CLUSTER_TYPE=source")
	}
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}
	name := cfg.BackupName
	if name == "auto" || strings.TrimSpace(name) == "" {
		name = "viya-full-backup-" + time.Now().Format("20060102-150405")
	}
	args := []string{"backup", "create", name,
		"--include-namespaces", cfg.ViyaNamespace,
		"--include-cluster-resources=true",
		"--csi-snapshot-timeout=4h",
		"--ttl=720h0m0s",
		"--namespace", cfg.VeleroNamespace,
	}
	if err := r.Run("velero", args...); err != nil {
		return err
	}
	state := LoadState()
	state.BackupName = name
	_ = state.Mark("backup", "created")
	return r.Run("velero", "backup", "describe", name, "--details", "--namespace", cfg.VeleroNamespace)
}

func createRestore(cfg *Config, r Runner) error {
	if cfg.ClusterType != "restore" {
		return fmt.Errorf("restore requires CLUSTER_TYPE=restore")
	}
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}
	backup := cfg.BackupName
	if strings.TrimSpace(backup) == "" || backup == "auto" {
		state := LoadState()
		backup = state.BackupName
	}
	if strings.TrimSpace(backup) == "" {
		return fmt.Errorf("BACKUP_NAME is required for restore or state.json must contain backupName")
	}
	restore := cfg.RestoreName
	if strings.TrimSpace(restore) == "" || restore == "auto" {
		restore = "viya-restore-" + time.Now().Format("20060102-150405")
	}
	args := []string{"restore", "create", restore,
		"--from-backup", backup,
		"--include-cluster-resources=true",
		"--namespace", cfg.VeleroNamespace,
	}
	if err := r.Run("velero", args...); err != nil {
		return err
	}
	state := LoadState()
	state.RestoreName = restore
	_ = state.Mark("restore", "created")
	return r.Run("velero", "restore", "describe", restore, "--details", "--namespace", cfg.VeleroNamespace)
}
