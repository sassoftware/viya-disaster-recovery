package main

import (
	"regexp"
	"testing"
	"time"
)

func TestVeleroResourceNameGeneratesTimestampedNames(t *testing.T) {
	first := veleroResourceName("auto", veleroBackupNamePrefix, time.Date(2026, 9, 2, 14, 3, 4, 5, time.UTC))
	second := veleroResourceName("auto", veleroBackupNamePrefix, time.Date(2026, 9, 2, 14, 3, 4, 6, time.UTC))

	pattern := regexp.MustCompile(`^viya-full-backup-\d{8}-\d{6}-\d{9}$`)
	if !pattern.MatchString(first) {
		t.Fatalf("backup name %q does not match expected timestamp format", first)
	}
	if !pattern.MatchString(second) {
		t.Fatalf("backup name %q does not match expected timestamp format", second)
	}
	if first == second {
		t.Fatalf("expected unique backup names for distinct timestamps, got %q", first)
	}
}

func TestVeleroResourceNameUsesRestorePrefix(t *testing.T) {
	name := veleroResourceName("", veleroRestoreNamePrefix, time.Date(2026, 9, 2, 14, 3, 4, 5, time.UTC))
	pattern := regexp.MustCompile(`^viya-full-restore-\d{8}-\d{6}-\d{9}$`)
	if !pattern.MatchString(name) {
		t.Fatalf("restore name %q does not match expected timestamp format", name)
	}
}

func TestVeleroResourceNameAvoidsBackupAndRestoreCollisions(t *testing.T) {
	names := map[string]bool{}
	base := time.Date(2026, 9, 2, 14, 3, 4, 0, time.UTC)

	for i := 0; i < 1000; i++ {
		backup := veleroResourceName("auto", veleroBackupNamePrefix, base.Add(time.Duration(i)*time.Nanosecond))
		restore := veleroResourceName("auto", veleroRestoreNamePrefix, base.Add(time.Duration(i)*time.Nanosecond))

		if names[backup] {
			t.Fatalf("duplicate backup name generated: %s", backup)
		}
		if names[restore] {
			t.Fatalf("duplicate restore name generated: %s", restore)
		}
		names[backup] = true
		names[restore] = true
	}
}

func TestVeleroResourceNamePreservesExplicitName(t *testing.T) {
	name := veleroResourceName(" custom-backup ", veleroBackupNamePrefix, time.Date(2026, 9, 2, 14, 3, 4, 5, time.UTC))
	if name != "custom-backup" {
		t.Fatalf("expected explicit name to be preserved, got %q", name)
	}
}