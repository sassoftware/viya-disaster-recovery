package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestProperties(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "environment.properties")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test properties file: %v", err)
	}
	return path
}

func TestLoadConfigNormalizesDeploymentTypeCasing(t *testing.T) {
	cases := map[string]string{
		"nmt": "nmt",
		"NMT": "nmt",
		"mt":  "mt",
		"MT":  "mt",
		" Mt": "mt",
	}
	for input, want := range cases {
		path := writeTestProperties(t, "KUBECONFIG_PATH=/tmp/kubeconfig\nVIYA_DEPLOYMENT_TYPE="+input+"\n")
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig(%q) failed: %v", input, err)
		}
		if cfg.ViyaDeploymentType != want {
			t.Fatalf("VIYA_DEPLOYMENT_TYPE=%q: got %q, want %q", input, cfg.ViyaDeploymentType, want)
		}
	}
}

func TestValidateForSetupAcceptsNMTAndMT(t *testing.T) {
	for _, deploymentType := range []string{deploymentTypeNMT, deploymentTypeMT} {
		cfg := &Config{
			KubeconfigPath:     "/tmp/kubeconfig",
			ClusterType:        "source",
			ViyaDeploymentType: deploymentType,
		}
		if err := cfg.ValidateForSetup(); err != nil {
			t.Fatalf("ValidateForSetup() with VIYA_DEPLOYMENT_TYPE=%q returned unexpected error: %v", deploymentType, err)
		}
	}
}

func TestValidateForSetupRejectsUnknownDeploymentType(t *testing.T) {
	cfg := &Config{
		KubeconfigPath:     "/tmp/kubeconfig",
		ClusterType:        "source",
		ViyaDeploymentType: "unsupported",
	}
	if err := cfg.ValidateForSetup(); err == nil {
		t.Fatal("expected error for unsupported VIYA_DEPLOYMENT_TYPE, got nil")
	}
}

func TestIsMultiTenant(t *testing.T) {
	if (&Config{ViyaDeploymentType: deploymentTypeMT}).IsMultiTenant() != true {
		t.Fatal("expected IsMultiTenant() to be true for mt")
	}
	if (&Config{ViyaDeploymentType: deploymentTypeNMT}).IsMultiTenant() != false {
		t.Fatal("expected IsMultiTenant() to be false for nmt")
	}
}
