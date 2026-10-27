// Copyright © 2025, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !health
// +build !health

package main

import (
	"os"
	"testing"
)

func TestGetEnvironmentNormalizesDeploymentType(t *testing.T) {
	dir := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prev) })

	for input, want := range map[string]string{"mt": "mt", "MT": "mt", "Nmt": "nmt", "nmt": "nmt"} {
		props := "MANUAL_SUBSCRIPTION_ID=sub\nVIYA_DEPLOYMENT_TYPE=" + input + "\n"
		if err := os.WriteFile("environment.properties", []byte(props), 0600); err != nil {
			t.Fatal(err)
		}
		if got := GetEnvironment().ViyaDeploymentType; got != want {
			t.Errorf("VIYA_DEPLOYMENT_TYPE=%q: got %q, want %q", input, got, want)
		}
	}

	if err := os.WriteFile("environment.properties", []byte("MANUAL_SUBSCRIPTION_ID=sub\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := GetEnvironment().ViyaDeploymentType; got != deploymentTypeNMT {
		t.Errorf("default deployment type: got %q, want %q", got, deploymentTypeNMT)
	}
}

func TestValidateViyaDeploymentType(t *testing.T) {
	for _, dt := range []string{deploymentTypeNMT, deploymentTypeMT} {
		for _, op := range []string{"Velero backup", "Velero restore", "Resource cleanup"} {
			if err := (Environment{ViyaDeploymentType: dt}).ValidateViyaDeploymentType(op); err != nil {
				t.Errorf("type %q op %q: unexpected error %v", dt, op, err)
			}
		}
	}
	if err := (Environment{ViyaDeploymentType: "unsupported"}).ValidateViyaDeploymentType("Velero backup"); err == nil {
		t.Error("expected error for unsupported deployment type")
	}
}

func TestIsMultiTenantAndLabel(t *testing.T) {
	mt := Environment{ViyaDeploymentType: deploymentTypeMT}
	nmt := Environment{ViyaDeploymentType: deploymentTypeNMT}
	if !mt.IsMultiTenant() || nmt.IsMultiTenant() {
		t.Error("IsMultiTenant returned the wrong value")
	}
	if mt.DeploymentTypeLabel() != "MT (Multi-Tenant)" || nmt.DeploymentTypeLabel() != "NMT (Non-Multi-Tenant)" {
		t.Error("unexpected deployment type label")
	}
}
