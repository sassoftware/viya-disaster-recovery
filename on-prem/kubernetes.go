package main

import (
	"fmt"
	"os"
)

func setupKubernetes(cfg *Config, r Runner) error {
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}
	crdURL := fmt.Sprintf("https://github.com/kubernetes-csi/external-snapshotter/client/config/crd?ref=%s", cfg.SnapshotterVersion)
	controllerURL := fmt.Sprintf("https://github.com/kubernetes-csi/external-snapshotter/deploy/kubernetes/snapshot-controller?ref=%s", cfg.SnapshotterVersion)
	if err := r.Run("kubectl", "apply", "-k", crdURL); err != nil {
		return err
	}
	if err := r.Run("kubectl", "apply", "-k", controllerURL); err != nil {
		return err
	}
	manifest := fmt.Sprintf(`apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: %s
  labels:
    velero.io/csi-volumesnapshot-class: "true"
driver: nfs.csi.k8s.io
deletionPolicy: Delete
`, cfg.NFSSnapshotClass)
	f, err := os.CreateTemp("", "nfs-snapshot-class-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(manifest); err != nil {
		return err
	}
	f.Close()
	return r.Run("kubectl", "apply", "-f", f.Name())
}

func checkKubernetesConnectivity(cfg *Config, r Runner) error {
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return err
	}
	if err := r.Run("kubectl", "cluster-info"); err != nil {
		return err
	}
	return r.Run("kubectl", "get", "nodes", "-o", "wide")
}
