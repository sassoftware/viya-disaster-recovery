package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	statusDeleted  = "Deleted"
	statusNotFound = "Not Found"
	statusSkipped  = "Skipped"
	statusFailed   = "Failed"
)

type cleanupRecord struct {
	Resource string
	Type     string
	Action   string
	Status   string
}

func cleanupDestroyResources(cfg *Config, r Runner) error {
	fmt.Println("Starting HPOS destroy cleanup...")
	report := make([]cleanupRecord, 0, 16)

	report = append(report, cleanupVeleroResources(cfg, r)...)
	report = append(report, cleanupLibrefsBucket(cfg, r)...)

	printCleanupReport(report)
	summary := cleanupSummary(report)
	fmt.Printf("\nCleanup Summary: %s\n", summary)

	if summary == "Success" {
		return nil
	}
	if summary == "Partial Success" {
		return fmt.Errorf("cleanup completed with partial success")
	}
	return fmt.Errorf("cleanup failed")
}

func cleanupVeleroResources(cfg *Config, r Runner) []cleanupRecord {
	records := make([]cleanupRecord, 0, 12)
	if err := os.Setenv("KUBECONFIG", cfg.KubeconfigPath); err != nil {
		return append(records, cleanupRecord{
			Resource: cfg.VeleroNamespace,
			Type:     "Velero",
			Action:   "Cleanup resources",
			Status:   statusFailed,
		})
	}

	if _, err := r.Output("kubectl", "cluster-info"); err != nil {
		return append(records, cleanupRecord{
			Resource: cfg.VeleroNamespace,
			Type:     "Velero",
			Action:   "Cleanup resources",
			Status:   statusSkipped,
		})
	}

	if _, err := r.Output("velero", "uninstall", "--namespace", cfg.VeleroNamespace, "--force"); err != nil {
		records = append(records, cleanupRecord{
			Resource: "velero uninstall",
			Type:     "Command",
			Action:   "Uninstall",
			Status:   statusSkipped,
		})
	} else {
		records = append(records, cleanupRecord{
			Resource: "velero uninstall",
			Type:     "Command",
			Action:   "Uninstall",
			Status:   statusDeleted,
		})
	}

	resources := []string{
		"backupstoragelocations.velero.io",
		"volumesnapshotlocations.velero.io",
		"backups.velero.io",
		"restores.velero.io",
		"schedules.velero.io",
		"backuprepositories.velero.io",
		"downloadrequests.velero.io",
		"deletebackuprequests.velero.io",
		"podvolumebackups.velero.io",
		"podvolumerestores.velero.io",
	}

	for _, resource := range resources {
		status := statusDeleted
		out, err := r.Output("kubectl", "delete", resource, "--all", "-n", cfg.VeleroNamespace, "--ignore-not-found")
		if err != nil {
			if isNotFoundError(err.Error() + "\n" + out) {
				status = statusNotFound
			} else {
				status = statusFailed
			}
		} else if isNotFoundError(out) {
			status = statusNotFound
		}

		records = append(records, cleanupRecord{
			Resource: resource,
			Type:     "Velero CR",
			Action:   "Delete all",
			Status:   status,
		})
	}

	nsOut, nsErr := r.Output("kubectl", "delete", "namespace", cfg.VeleroNamespace, "--ignore-not-found")
	nsStatus := statusDeleted
	if nsErr != nil {
		if isNotFoundError(nsErr.Error() + "\n" + nsOut) {
			nsStatus = statusNotFound
		} else {
			nsStatus = statusFailed
		}
	} else if isNotFoundError(nsOut) {
		nsStatus = statusNotFound
	}

	records = append(records, cleanupRecord{
		Resource: cfg.VeleroNamespace,
		Type:     "Namespace",
		Action:   "Delete",
		Status:   nsStatus,
	})

	return records
}

func cleanupLibrefsBucket(cfg *Config, r Runner) []cleanupRecord {
	records := make([]cleanupRecord, 0, 4)
	if _, err := r.Output("aws", "--version"); err != nil {
		return append(records,
			cleanupRecord{
				Resource: cfg.LibrefsBucket + "/*",
				Type:     "Object",
				Action:   "Delete contents",
				Status:   statusFailed,
			},
			cleanupRecord{
				Resource: cfg.LibrefsBucket,
				Type:     "Bucket",
				Action:   "Delete",
				Status:   statusFailed,
			},
		)
	}

	if err := r.Shell(fmt.Sprintf("AWS_ACCESS_KEY_ID=%s AWS_SECRET_ACCESS_KEY=%s AWS_DEFAULT_REGION=minio AWS_EC2_METADATA_DISABLED=true aws --endpoint-url %s s3api head-bucket --bucket %s",
		shellQuote(cfg.LibrefsAccessKey),
		shellQuote(cfg.LibrefsSecretKey),
		shellQuote(cfg.LibrefsEndpoint),
		shellQuote(cfg.LibrefsBucket),
	)); err != nil {
		if isNotFoundError(err.Error()) {
			return append(records,
				cleanupRecord{
					Resource: cfg.LibrefsBucket + "/*",
					Type:     "Object",
					Action:   "Delete contents",
					Status:   statusNotFound,
				},
				cleanupRecord{
					Resource: cfg.LibrefsBucket,
					Type:     "Bucket",
					Action:   "Delete",
					Status:   statusNotFound,
				},
			)
		}

		return append(records,
			cleanupRecord{
				Resource: cfg.LibrefsBucket + "/*",
				Type:     "Object",
				Action:   "Delete contents",
				Status:   statusFailed,
			},
			cleanupRecord{
				Resource: cfg.LibrefsBucket,
				Type:     "Bucket",
				Action:   "Delete",
				Status:   statusFailed,
			},
		)
	}

	contentStatus := statusDeleted
	if err := r.Shell(fmt.Sprintf("AWS_ACCESS_KEY_ID=%s AWS_SECRET_ACCESS_KEY=%s AWS_DEFAULT_REGION=minio AWS_EC2_METADATA_DISABLED=true aws --endpoint-url %s s3 rm s3://%s --recursive",
		shellQuote(cfg.LibrefsAccessKey),
		shellQuote(cfg.LibrefsSecretKey),
		shellQuote(cfg.LibrefsEndpoint),
		shellQuote(cfg.LibrefsBucket),
	)); err != nil {
		if isNotFoundError(err.Error()) {
			contentStatus = statusNotFound
		} else {
			contentStatus = statusFailed
		}
	}
	records = append(records, cleanupRecord{
		Resource: cfg.LibrefsBucket + "/*",
		Type:     "Object",
		Action:   "Delete contents",
		Status:   contentStatus,
	})

	bucketStatus := statusDeleted
	if err := r.Shell(fmt.Sprintf("AWS_ACCESS_KEY_ID=%s AWS_SECRET_ACCESS_KEY=%s AWS_DEFAULT_REGION=minio AWS_EC2_METADATA_DISABLED=true aws --endpoint-url %s s3 rb s3://%s",
		shellQuote(cfg.LibrefsAccessKey),
		shellQuote(cfg.LibrefsSecretKey),
		shellQuote(cfg.LibrefsEndpoint),
		shellQuote(cfg.LibrefsBucket),
	)); err != nil {
		if isNotFoundError(err.Error()) {
			bucketStatus = statusNotFound
		} else {
			bucketStatus = statusFailed
		}
	}
	records = append(records, cleanupRecord{
		Resource: cfg.LibrefsBucket,
		Type:     "Bucket",
		Action:   "Delete",
		Status:   bucketStatus,
	})

	return records
}

func isNotFoundError(message string) bool {
	msg := strings.ToLower(message)
	patterns := []string{
		"not found",
		"no resources found",
		"the server doesn't have a resource type",
		"the server could not find the requested resource",
		"no matches for kind",
		"nosuchbucket",
		"404",
		"status code: 404",
		"cannot find",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func printCleanupReport(report []cleanupRecord) {
	fmt.Println("\nCleanup Report")
	fmt.Println("<Resource> | <Type> | <Action> | <Status>")
	for _, rec := range report {
		fmt.Printf("%s | %s | %s | %s\n", rec.Resource, rec.Type, rec.Action, rec.Status)
	}
}

func cleanupSummary(report []cleanupRecord) string {
	if len(report) == 0 {
		return "Success"
	}
	failed := 0
	successful := 0
	for _, rec := range report {
		switch rec.Status {
		case statusFailed:
			failed++
		case statusDeleted, statusNotFound, statusSkipped:
			successful++
		}
	}
	if failed == 0 {
		return "Success"
	}
	if successful == 0 {
		return "Failed"
	}
	return "Partial Success"
}
