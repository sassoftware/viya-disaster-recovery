package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func setupHPOS(cfg *Config, r Runner) error {
	switch strings.ToLower(cfg.LibrefsInstallMode) {
	case "skip":
		fmt.Println("Skipping libreFS installation because LIBREFS_INSTALL_MODE=skip")
		return nil
	case "local":
		return setupLibrefsLocal(cfg, r)
	case "remote":
		return setupLibrefsRemote(cfg, r)
	default:
		return fmt.Errorf("LIBREFS_INSTALL_MODE must be skip, local, or remote")
	}
}

func setupLibrefsLocal(cfg *Config, r Runner) error {
	if _, err := os.Stat(cfg.LibrefsBinaryPath); err != nil {
		return fmt.Errorf("libreFS binary not found at %s: %w", cfg.LibrefsBinaryPath, err)
	}
	service := renderLibrefsService(cfg)
	tmpService := filepath.Join(os.TempDir(), cfg.LibrefsServiceName+".service")
	if err := os.WriteFile(tmpService, []byte(service), 0644); err != nil {
		return err
	}
	script := fmt.Sprintf(`set -e
sudo mkdir -p %[1]q %[2]q
sudo cp %[3]q %[1]q/librefs
sudo chmod +x %[1]q/librefs
if ! id %[4]q >/dev/null 2>&1; then sudo useradd -r -s /sbin/nologin %[4]q; fi
sudo chown -R %[4]q:%[4]q %[1]q %[2]q
sudo cp %[5]q /etc/systemd/system/%[6]s.service
sudo systemctl daemon-reload
sudo systemctl enable %[6]s
sudo systemctl restart %[6]s
sudo systemctl --no-pager status %[6]s
if command -v firewall-cmd >/dev/null 2>&1 && sudo firewall-cmd --state >/dev/null 2>&1; then
  sudo firewall-cmd --permanent --add-port=%[7]s/tcp
  sudo firewall-cmd --permanent --add-port=%[8]s/tcp
  sudo firewall-cmd --reload
fi`, cfg.LibrefsOptDir, cfg.LibrefsDataDir, cfg.LibrefsBinaryPath, cfg.LibrefsUser, tmpService, cfg.LibrefsServiceName, cfg.LibrefsAPIPort, cfg.LibrefsConsolePort)
	return r.Shell(script)
}

func setupLibrefsRemote(cfg *Config, r Runner) error {
	if cfg.LibrefsRemoteHost == "" {
		return fmt.Errorf("LIBREFS_REMOTE_HOST is required when LIBREFS_INSTALL_MODE=remote")
	}
	keyArg := ""
	if cfg.LibrefsRemoteKeyPath != "" {
		keyArg = "-i " + shellQuote(cfg.LibrefsRemoteKeyPath)
	}
	remote := cfg.LibrefsRemoteUser + "@" + cfg.LibrefsRemoteHost
	service := renderLibrefsService(cfg)
	tmpService := filepath.Join(os.TempDir(), cfg.LibrefsServiceName+".service")
	if err := os.WriteFile(tmpService, []byte(service), 0644); err != nil {
		return err
	}
	if err := r.Shell(fmt.Sprintf("scp %s %s %s:/tmp/librefs", keyArg, shellQuote(cfg.LibrefsBinaryPath), shellQuote(remote))); err != nil {
		return err
	}
	if err := r.Shell(fmt.Sprintf("scp %s %s %s:/tmp/%s.service", keyArg, shellQuote(tmpService), shellQuote(remote), cfg.LibrefsServiceName)); err != nil {
		return err
	}
	remoteScript := fmt.Sprintf(`set -e
sudo mkdir -p %[1]q %[2]q
sudo mv /tmp/librefs %[1]q/librefs
sudo chmod +x %[1]q/librefs
if ! id %[3]q >/dev/null 2>&1; then sudo useradd -r -s /sbin/nologin %[3]q; fi
sudo chown -R %[3]q:%[3]q %[1]q %[2]q
sudo mv /tmp/%[4]s.service /etc/systemd/system/%[4]s.service
sudo systemctl daemon-reload
sudo systemctl enable %[4]s
sudo systemctl restart %[4]s
sudo systemctl --no-pager status %[4]s
if command -v firewall-cmd >/dev/null 2>&1 && sudo firewall-cmd --state >/dev/null 2>&1; then
  sudo firewall-cmd --permanent --add-port=%[5]s/tcp
  sudo firewall-cmd --permanent --add-port=%[6]s/tcp
  sudo firewall-cmd --reload
fi`, cfg.LibrefsOptDir, cfg.LibrefsDataDir, cfg.LibrefsUser, cfg.LibrefsServiceName, cfg.LibrefsAPIPort, cfg.LibrefsConsolePort)
	return r.Shell(fmt.Sprintf("ssh %s %s %s", keyArg, shellQuote(remote), shellQuote(remoteScript)))
}

func renderLibrefsService(cfg *Config) string {
	return fmt.Sprintf(`[Unit]
Description=libreFS Object Storage
After=network.target

[Service]
Type=simple
User=%s
Group=%s
Environment=MINIO_ROOT_USER=%s
Environment=MINIO_ROOT_PASSWORD=%s
ExecStart=%s/librefs server %s --console-address %s
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, cfg.LibrefsUser, cfg.LibrefsUser, cfg.LibrefsAccessKey, cfg.LibrefsSecretKey, cfg.LibrefsOptDir, cfg.LibrefsDataDir, cfg.LibrefsConsoleAddress)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
