package main

// legacy_checks.go
//
// Original SAS Viya health checks: readiness pod, CAS services, PostgreSQL
// clusters (Crunchydata) and Velero backups/restores. Recovered from the
// last known-good committed version of this module (git history) after the
// file on disk was overwritten with only the newer post-installation
// validation checks. Logic is unchanged from that version; only imports and
// formatting were reconciled with the rest of the package.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type ReadinessHealth struct {
	Status  string     `json:"status"`
	PodInfo PodDetails `json:"pod_info"`
	Message string     `json:"message"`
}

type CASHealth struct {
	Status         string           `json:"status"`
	Message        string           `json:"message"`
	SetupType      string           `json:"setup_type"`
	ComponentCount ComponentSummary `json:"component_summary"`
	Components     CASComponents    `json:"components"`
}

type PostgreSQLHealth struct {
	Status         string              `json:"status"`
	Message        string              `json:"message"`
	ClustersFound  int                 `json:"clusters_found"`
	Clusters       []PostgreSQLCluster `json:"clusters"`
	ComponentCount ComponentSummary    `json:"component_summary"`
}

type PostgreSQLCluster struct {
	Name         string             `json:"name"`
	Status       string             `json:"status"`
	Message      string             `json:"message"`
	Leader       PostgreSQLInstance `json:"leader,omitempty"`
	Replicas     []PodDetails       `json:"replicas"`
	RepoHost     PodDetails         `json:"repo_host,omitempty"`
	TotalPods    int                `json:"total_pods"`
	HealthyPods  int                `json:"healthy_pods"`
	Connectivity ConnectivityCheck  `json:"connectivity"`
	Databases    []DatabaseInfo     `json:"databases,omitempty"`
}

type PostgreSQLInstance struct {
	PodDetails
	IsLeader bool   `json:"is_leader"`
	Role     string `json:"role"`
}

type ConnectivityCheck struct {
	Status   string    `json:"status"`
	Message  string    `json:"message"`
	TestedAt time.Time `json:"tested_at"`
}

type DatabaseInfo struct {
	Name       string `json:"name"`
	Size       string `json:"size"`
	TableCount int    `json:"table_count"`
}

type TableInfo struct {
	TableName string `json:"table_name"`
	RowCount  int64  `json:"row_count"`
}

type DatabaseDetail struct {
	Name   string      `json:"database_name"`
	Size   string      `json:"database_size"`
	Tables []TableInfo `json:"tables"`
}

type PostgreSQLDatabaseReport struct {
	ClusterName string           `json:"cluster_name"`
	Timestamp   time.Time        `json:"timestamp"`
	Databases   []DatabaseDetail `json:"databases"`
}

type PodDetails struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Ready        string `json:"ready"`
	RestartCount string `json:"restart_count,omitempty"`
	Age          string `json:"age,omitempty"`
}

type ComponentSummary struct {
	Total    int `json:"total"`
	Healthy  int `json:"healthy"`
	Degraded int `json:"degraded"`
	Failed   int `json:"failed"`
}

type CASComponents struct {
	Control  PodDetails `json:"control"`
	Operator PodDetails `json:"operator"`
	Server   CASServer  `json:"server"`
}

type CASServer struct {
	Controller PodDetails   `json:"controller"`
	Workers    []PodDetails `json:"workers"`
	Backup     PodDetails   `json:"backup,omitempty"`
}

type VeleroHealth struct {
	Status            string        `json:"status"`
	Message           string        `json:"message"`
	InstallationFound bool          `json:"installation_found"`
	BackupsFound      int           `json:"backups_found"`
	LatestBackup      *VeleroBackup `json:"latest_backup,omitempty"`
}

type VeleroBackup struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Created    time.Time `json:"created"`
	DetailFile string    `json:"detail_file,omitempty"`
}

// checkReadinessPod - Check only the SAS readiness pod
func checkReadinessPod(namespace, kubeconfig string) ReadinessHealth {
	readiness := ReadinessHealth{}

	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace, "-l", "app=sas-readiness",
		"-o", "jsonpath={.items[0].metadata.name},{.items[0].status.phase},{.items[0].status.containerStatuses[0].restartCount},{.items[0].status.containerStatuses[0].ready}")

	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	output, err := cmd.Output()
	if err != nil {
		readiness.Status = "UNHEALTHY"
		readiness.Message = "No SAS readiness pod found with label app=sas-readiness"
		return readiness
	}

	parts := strings.Split(string(output), ",")
	if len(parts) != 4 {
		readiness.Status = "UNHEALTHY"
		readiness.Message = "Could not parse readiness pod info"
		return readiness
	}

	podName, phase, restarts, ready := parts[0], parts[1], parts[2], parts[3]
	readiness.PodInfo = PodDetails{
		Name:         podName,
		Status:       phase,
		Ready:        ready,
		RestartCount: restarts,
	}

	if phase == "Running" && ready == "true" {
		if restarts == "0" {
			readiness.Status = "HEALTHY"
			readiness.Message = "Pod running with no restarts"
		} else {
			readiness.Status = "DEGRADED"
			readiness.Message = fmt.Sprintf("Pod running but has %s restarts", restarts)
		}
	} else {
		readiness.Status = "UNHEALTHY"
		readiness.Message = fmt.Sprintf("Pod in %s phase, ready=%s", phase, ready)
	}

	return readiness
}

// checkCASHealth - Check all CAS related pods
func checkCASHealth(namespace, kubeconfig string) CASHealth {
	casHealth := CASHealth{
		Components: CASComponents{},
	}

	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace, "--no-headers")

	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	output, err := cmd.Output()
	if err != nil {
		casHealth.Status = "UNHEALTHY"
		casHealth.Message = "Failed to get pod information"
		casHealth.ComponentCount = ComponentSummary{Total: 0, Healthy: 0, Degraded: 0, Failed: 1}
		return casHealth
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	var casControlFound, casOperatorFound bool
	var casServerController bool
	var casWorkers []PodDetails
	var unhealthyPods []string
	var totalComponents, healthyComponents, degradedComponents, failedComponents int

	for _, line := range lines {
		if strings.Contains(line, "cas-") {
			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}

			podName := parts[0]
			ready := parts[1]
			status := parts[2]

			podInfo := PodDetails{
				Name:   podName,
				Status: status,
				Ready:  ready,
			}

			totalComponents++

			readyParts := strings.Split(ready, "/")
			isHealthy := false
			if status == "Running" && len(readyParts) == 2 {
				isHealthy = readyParts[0] == readyParts[1]
			}

			if !isHealthy {
				unhealthyPods = append(unhealthyPods, podName)
				failedComponents++
			} else {
				healthyComponents++
			}

			switch {
			case strings.Contains(podName, "sas-cas-control"):
				casHealth.Components.Control = podInfo
				casControlFound = true
			case strings.Contains(podName, "sas-cas-operator"):
				casHealth.Components.Operator = podInfo
				casOperatorFound = true
			case strings.Contains(podName, "sas-cas-server") && strings.Contains(podName, "controller"):
				casHealth.Components.Server.Controller = podInfo
				casServerController = true
			case strings.Contains(podName, "sas-cas-server") && strings.Contains(podName, "backup"):
				casHealth.Components.Server.Backup = podInfo
			case strings.Contains(podName, "sas-cas-server") && strings.Contains(podName, "worker"):
				casHealth.Components.Server.Workers = append(casHealth.Components.Server.Workers, podInfo)
				casWorkers = append(casWorkers, podInfo)
			}
		}
	}

	casHealth.ComponentCount = ComponentSummary{
		Total:    totalComponents,
		Healthy:  healthyComponents,
		Degraded: degradedComponents,
		Failed:   failedComponents,
	}

	if len(casWorkers) > 0 {
		casHealth.SetupType = "MPP"
	} else {
		casHealth.SetupType = "SMP"
	}

	if !casControlFound || !casOperatorFound || !casServerController {
		casHealth.Status = "UNHEALTHY"
		missing := []string{}
		if !casControlFound {
			missing = append(missing, "cas-control")
		}
		if !casOperatorFound {
			missing = append(missing, "cas-operator")
		}
		if !casServerController {
			missing = append(missing, "cas-server-controller")
		}
		casHealth.Message = fmt.Sprintf("Missing CAS components: %s", strings.Join(missing, ", "))
	} else if len(unhealthyPods) > 0 {
		casHealth.Status = "UNHEALTHY"
		casHealth.Message = fmt.Sprintf("CAS pods not running: %s", strings.Join(unhealthyPods, ", "))
	} else {
		casHealth.Status = "HEALTHY"
		workerCount := len(casWorkers)
		casHealth.Message = fmt.Sprintf("All CAS services healthy (%s setup with %d workers)", casHealth.SetupType, workerCount)
	}

	return casHealth
}

// checkPostgreSQLHealth - Simple PostgreSQL cluster check using role labels
func checkPostgreSQLHealth(namespace, kubeconfig string) PostgreSQLHealth {
	pgHealth := PostgreSQLHealth{
		Clusters: []PostgreSQLCluster{},
	}

	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", "postgres-operator.crunchydata.com/cluster",
		"--no-headers", "-o", "jsonpath={.items[*].metadata.labels['postgres-operator\\.crunchydata\\.com/cluster']}")

	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	output, err := cmd.Output()
	if err != nil {
		pgHealth.Status = "UNHEALTHY"
		pgHealth.Message = "Failed to get PostgreSQL clusters"
		pgHealth.ClustersFound = 0
		pgHealth.ComponentCount = ComponentSummary{Total: 0, Healthy: 0, Degraded: 0, Failed: 1}
		return pgHealth
	}

	clusterNamesStr := strings.TrimSpace(string(output))
	if clusterNamesStr == "" {
		pgHealth.Status = "HEALTHY"
		pgHealth.Message = "No PostgreSQL clusters found"
		pgHealth.ClustersFound = 0
		pgHealth.ComponentCount = ComponentSummary{Total: 0, Healthy: 0, Degraded: 0, Failed: 0}
		return pgHealth
	}

	clusterNames := make(map[string]bool)
	for _, name := range strings.Fields(clusterNamesStr) {
		if name != "" {
			clusterNames[name] = true
		}
	}

	var totalHealthy, totalDegraded, totalFailed int

	for clusterName := range clusterNames {
		cluster := checkClusterByRoles(clusterName, namespace, kubeconfig)

		switch cluster.Status {
		case "HEALTHY":
			totalHealthy++
		case "DEGRADED":
			totalDegraded++
		case "UNHEALTHY":
			totalFailed++
		}

		pgHealth.Clusters = append(pgHealth.Clusters, cluster)
	}

	pgHealth.ClustersFound = len(pgHealth.Clusters)
	pgHealth.ComponentCount = ComponentSummary{
		Total:    len(pgHealth.Clusters),
		Healthy:  totalHealthy,
		Degraded: totalDegraded,
		Failed:   totalFailed,
	}

	if totalFailed > 0 {
		pgHealth.Status = "UNHEALTHY"
		pgHealth.Message = fmt.Sprintf("%d cluster(s) failed, %d healthy", totalFailed, totalHealthy)
	} else if totalDegraded > 0 {
		pgHealth.Status = "DEGRADED"
		pgHealth.Message = fmt.Sprintf("%d cluster(s) degraded, %d healthy", totalDegraded, totalHealthy)
	} else {
		pgHealth.Status = "HEALTHY"
		pgHealth.Message = fmt.Sprintf("All %d PostgreSQL cluster(s) healthy", totalHealthy)
	}

	return pgHealth
}

// checkClusterByRoles - Simple approach using role labels directly
func checkClusterByRoles(clusterName, namespace, kubeconfig string) PostgreSQLCluster {
	cluster := PostgreSQLCluster{
		Name: clusterName,
	}

	masterCmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", fmt.Sprintf("postgres-operator.crunchydata.com/cluster=%s,postgres-operator.crunchydata.com/role=master", clusterName),
		"--no-headers")
	if kubeconfig != "" {
		masterCmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	masterOutput, err := masterCmd.Output()
	if err != nil || strings.TrimSpace(string(masterOutput)) == "" {
		cluster.Status = "UNHEALTHY"
		cluster.Message = "No master pod found"
		return cluster
	}

	masterLine := strings.TrimSpace(string(masterOutput))
	masterParts := strings.Fields(masterLine)
	if len(masterParts) >= 3 {
		cluster.Leader = PostgreSQLInstance{
			PodDetails: PodDetails{
				Name:   masterParts[0],
				Ready:  masterParts[1],
				Status: masterParts[2],
			},
			IsLeader: true,
			Role:     "master",
		}
	}

	replicaCmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", fmt.Sprintf("postgres-operator.crunchydata.com/cluster=%s,postgres-operator.crunchydata.com/role=replica", clusterName),
		"--no-headers")
	if kubeconfig != "" {
		replicaCmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	replicaOutput, _ := replicaCmd.Output()
	replicaLines := strings.Split(strings.TrimSpace(string(replicaOutput)), "\n")

	var replicas []PodDetails
	for _, line := range replicaLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			replicas = append(replicas, PodDetails{
				Name:   parts[0],
				Ready:  parts[1],
				Status: parts[2],
			})
		}
	}
	cluster.Replicas = replicas

	repoCmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", fmt.Sprintf("postgres-operator.crunchydata.com/cluster=%s", clusterName),
		"--no-headers")
	if kubeconfig != "" {
		repoCmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	repoOutput, _ := repoCmd.Output()
	repoLines := strings.Split(strings.TrimSpace(string(repoOutput)), "\n")

	for _, line := range repoLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 3 && strings.Contains(parts[0], "repo-host") {
			cluster.RepoHost = PodDetails{
				Name:   parts[0],
				Ready:  parts[1],
				Status: parts[2],
			}
			break
		}
	}

	totalPods := 1 + len(replicas)
	if cluster.RepoHost.Name != "" {
		totalPods++
	}
	cluster.TotalPods = totalPods

	healthyPods := 0
	if isHealthyPod(cluster.Leader.Status, cluster.Leader.Ready) {
		healthyPods++
	}
	for _, replica := range replicas {
		if isHealthyPod(replica.Status, replica.Ready) {
			healthyPods++
		}
	}
	if cluster.RepoHost.Name != "" && isHealthyPod(cluster.RepoHost.Status, cluster.RepoHost.Ready) {
		healthyPods++
	}
	cluster.HealthyPods = healthyPods

	if cluster.Leader.Name != "" {
		cluster.Connectivity = testPostgreSQLConnectivity(cluster.Leader.Name, namespace, kubeconfig)
		cluster.Databases = getPostgreSQLDatabaseDetails(clusterName, cluster.Leader.Name, namespace, kubeconfig)
	}

	if cluster.Leader.Name == "" {
		cluster.Status = "UNHEALTHY"
		cluster.Message = "No master pod found"
	} else if healthyPods == 0 {
		cluster.Status = "UNHEALTHY"
		cluster.Message = "All pods are unhealthy"
	} else if healthyPods < totalPods {
		cluster.Status = "DEGRADED"
		cluster.Message = fmt.Sprintf("%d/%d pods healthy", healthyPods, totalPods)
	} else {
		cluster.Status = "HEALTHY"
		cluster.Message = fmt.Sprintf("All pods healthy - master: %s, replicas: %d", cluster.Leader.Name, len(replicas))
	}

	return cluster
}

func isHealthyPod(status, ready string) bool {
	readyParts := strings.Split(ready, "/")
	return status == "Running" && len(readyParts) == 2 && readyParts[0] == readyParts[1]
}

// testPostgreSQLConnectivity tests basic connectivity to PostgreSQL cluster
func testPostgreSQLConnectivity(leaderPodName, namespace, kubeconfig string) ConnectivityCheck {
	connectivity := ConnectivityCheck{
		TestedAt: time.Now(),
	}

	if leaderPodName == "" {
		connectivity.Status = "UNHEALTHY"
		connectivity.Message = "No leader pod available"
		return connectivity
	}

	testCmd := exec.Command("kubectl", "exec", "-n", namespace, leaderPodName,
		"--", "pg_isready", "-U", "postgres")

	if kubeconfig != "" {
		testCmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	if err := testCmd.Run(); err == nil {
		connectivity.Status = "HEALTHY"
		connectivity.Message = fmt.Sprintf("PostgreSQL connectivity verified on leader: %s", leaderPodName)
		return connectivity
	}

	connectivity.Status = "UNHEALTHY"
	connectivity.Message = fmt.Sprintf("Leader pod found (%s) but connectivity test failed", leaderPodName)
	return connectivity
}

// getPostgreSQLDatabaseDetails retrieves detailed database information for a cluster
func getPostgreSQLDatabaseDetails(clusterName, leaderPodName, namespace, kubeconfig string) []DatabaseInfo {
	var databases []DatabaseInfo

	if leaderPodName == "" {
		fmt.Printf("   Warning: No leader pod found for cluster %s\n", clusterName)
		return databases
	}

	fmt.Printf("   Getting database details from leader pod: %s\n", leaderPodName)

	listDbCmd := exec.Command("kubectl", "exec", "-n", namespace, leaderPodName, "--",
		"psql", "-U", "postgres", "-t", "-c",
		"SELECT datname, pg_size_pretty(pg_database_size(datname)) FROM pg_database WHERE datistemplate = false ORDER BY datname;")
	if kubeconfig != "" {
		listDbCmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	dbListOutput, err := listDbCmd.Output()
	if err != nil {
		fmt.Printf("   Warning: Failed to get database list: %v\n", err)
		return databases
	}

	dbLines := strings.Split(strings.TrimSpace(string(dbListOutput)), "\n")

	for _, dbLine := range dbLines {
		dbLine = strings.TrimSpace(dbLine)
		if dbLine == "" {
			continue
		}

		parts := strings.Split(dbLine, "|")
		if len(parts) != 2 {
			continue
		}

		dbName := strings.TrimSpace(parts[0])
		dbSize := strings.TrimSpace(parts[1])

		fmt.Printf("     Processing database: %s (Size: %s)\n", dbName, dbSize)

		tableQuery := `
		SELECT
		    schemaname || '.' || relname       AS table_name,
		    n_live_tup                          AS approx_rows,
		    pg_size_pretty(pg_total_relation_size(relid)) AS total_size
		FROM pg_stat_all_tables
		WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
		ORDER BY pg_total_relation_size(relid) DESC;`

		tableCmd := exec.Command("kubectl", "exec", "-n", namespace, leaderPodName, "--",
			"psql", "-U", "postgres", "-d", dbName, "-t", "-c", tableQuery)
		if kubeconfig != "" {
			tableCmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		}

		tableOutput, err := tableCmd.Output()
		var tableCount int
		var tableDets []TableInfo

		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(tableOutput)), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				parts := strings.Split(line, "|")
				if len(parts) >= 3 {
					tableName := strings.TrimSpace(parts[0])
					var estimatedRows int64
					totalSize := strings.TrimSpace(parts[2])

					fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &estimatedRows)

					tableNameWithSizeInfo := fmt.Sprintf("%s (Total: %s)", tableName, totalSize)

					tableDets = append(tableDets, TableInfo{
						TableName: tableNameWithSizeInfo,
						RowCount:  estimatedRows,
					})
					tableCount++
				}
			}
		} else {
			fmt.Printf("       Warning: Could not get table details for database %s: %v\n", dbName, err)
		}

		databases = append(databases, DatabaseInfo{
			Name:       dbName,
			Size:       dbSize,
			TableCount: tableCount,
		})

		if len(tableDets) > 0 {
			generateDatabaseReportFile(clusterName, namespace, dbName, dbSize, tableDets)
		}

		fmt.Printf("       Found %d tables in database %s\n", tableCount, dbName)
	}

	return databases
}

// generateDatabaseReportFile creates the database report
func generateDatabaseReportFile(clusterName, namespace, dbName, dbSize string, tables []TableInfo) {
	timestamp := time.Now().Format("20060102-150405")

	mainFilename := fmt.Sprintf("postgres-database-details-%s-%s-%s.json", clusterName, namespace, timestamp)

	existingFiles, err := os.ReadDir(".")
	if err == nil {
		todayDate := timestamp[:8]
		for _, file := range existingFiles {
			fileName := file.Name()
			if strings.HasPrefix(fileName, fmt.Sprintf("postgres-database-details-%s-%s", clusterName, namespace)) &&
				strings.Contains(fileName, todayDate) {
				mainFilename = fileName
				break
			}
		}
	}

	var report PostgreSQLDatabaseReport
	if existingData, err := os.ReadFile(mainFilename); err == nil {
		json.Unmarshal(existingData, &report)
	} else {
		report = PostgreSQLDatabaseReport{
			ClusterName: clusterName,
			Timestamp:   time.Now(),
			Databases:   []DatabaseDetail{},
		}
	}

	dbDetail := DatabaseDetail{
		Name:   dbName,
		Size:   dbSize,
		Tables: tables,
	}

	found := false
	for i, existing := range report.Databases {
		if existing.Name == dbName {
			report.Databases[i] = dbDetail
			found = true
			break
		}
	}

	if !found {
		report.Databases = append(report.Databases, dbDetail)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		if err := os.WriteFile(mainFilename, data, 0644); err == nil {
			fmt.Printf("   ✓ Generated database report: %s\n", mainFilename)
			fmt.Printf("     Database: %s, Size: %s, Tables: %d\n", dbName, dbSize, len(tables))
		}
	}
}

// checkVeleroHealth - Interactive Velero backup/restore check
func checkVeleroHealth(namespace, kubeconfig string) VeleroHealth {
	veleroHealth := VeleroHealth{
		BackupsFound: 0,
	}

	fmt.Printf("     Checking for Velero namespace...\n")

	cmd := exec.Command("kubectl", "get", "namespace", "velero", "--no-headers")
	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	_, err := cmd.Output()
	if err != nil {
		fmt.Printf("     Velero namespace not found\n")
		veleroHealth.Status = "UNHEALTHY"
		veleroHealth.Message = "Velero namespace not found - Velero not installed"
		veleroHealth.InstallationFound = false
		return veleroHealth
	}

	veleroHealth.InstallationFound = true
	fmt.Printf("     Velero namespace found\n")

	fmt.Printf("\n%s%s┌─ VELERO INTERACTIVE CHECK %s%s\n", ColorBold, ColorCyan, strings.Repeat("─", 51), ColorReset)
	fmt.Printf("%s│%s What would you like to check? %s%s\n", ColorCyan, ColorReset, ColorCyan, ColorReset)
	fmt.Printf("%s│%s 1. Backups %s%s\n", ColorCyan, ColorReset, ColorCyan, ColorReset)
	fmt.Printf("%s│%s 2. Restores %s%s\n", ColorCyan, ColorReset, ColorCyan, ColorReset)
	fmt.Printf("%s└%s%s\n", ColorCyan, strings.Repeat("─", 79), ColorReset)

	choice := promptForInput("Enter your choice (1 for backups, 2 for restores): ")

	if choice == "1" {
		return checkVeleroBackups(kubeconfig, veleroHealth)
	} else if choice == "2" {
		return checkVeleroRestores(kubeconfig, veleroHealth)
	} else {
		veleroHealth.Status = "DEGRADED"
		veleroHealth.Message = "Invalid choice - defaulting to backup check"
		return checkVeleroBackups(kubeconfig, veleroHealth)
	}
}

// checkVeleroBackups - Check and select specific backup
func checkVeleroBackups(kubeconfig string, veleroHealth VeleroHealth) VeleroHealth {
	fmt.Printf("\n     Checking for Velero backups...\n")

	cmd := exec.Command("kubectl", "get", "backups.velero.io", "-A", "--no-headers",
		"--sort-by=.metadata.creationTimestamp", "-o",
		"custom-columns=NAME:.metadata.name,PHASE:.status.phase,CREATED:.metadata.creationTimestamp")
	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	backupOutput, err := cmd.Output()
	if err != nil {
		veleroHealth.Status = "HEALTHY"
		veleroHealth.Message = "Velero installed but no backups found or CRDs not available"
		return veleroHealth
	}

	backupLines := strings.Split(strings.TrimSpace(string(backupOutput)), "\n")
	if len(backupLines) == 1 && strings.TrimSpace(backupLines[0]) == "" {
		veleroHealth.Status = "HEALTHY"
		veleroHealth.Message = "Velero installed but no backups found"
		return veleroHealth
	}

	veleroHealth.BackupsFound = len(backupLines)

	fmt.Printf("\n%s%s Available Backups:%s\n", ColorBold, ColorGreen, ColorReset)
	for i, line := range backupLines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 3 {
			fmt.Printf("  %d. %s (Phase: %s, Created: %s)\n", i+1, fields[0], fields[1], fields[2])
		}
	}

	backupChoice := promptForInput(fmt.Sprintf("\nSelect backup number (1-%d) or press Enter for latest: ", len(backupLines)))

	var selectedLine string
	if backupChoice == "" {
		selectedLine = strings.TrimSpace(backupLines[len(backupLines)-1])
	} else {
		choice, err := strconv.Atoi(backupChoice)
		if err != nil || choice < 1 || choice > len(backupLines) {
			veleroHealth.Status = "DEGRADED"
			veleroHealth.Message = "Invalid backup selection - using latest"
			selectedLine = strings.TrimSpace(backupLines[len(backupLines)-1])
		} else {
			selectedLine = strings.TrimSpace(backupLines[choice-1])
		}
	}

	fields := strings.Fields(selectedLine)
	if len(fields) >= 3 {
		backupName := fields[0]
		phase := fields[1]
		createdStr := fields[2]

		created, _ := time.Parse("2006-01-02T15:04:05Z", createdStr)

		fmt.Printf("\n     Selected backup: %s (Phase: %s)\n", backupName, phase)

		detailFile := generateBackupDetailFile(backupName, kubeconfig)

		veleroHealth.LatestBackup = &VeleroBackup{
			Name:       backupName,
			Status:     determineSimpleBackupStatus(phase),
			Created:    created,
			DetailFile: detailFile,
		}

		if veleroHealth.LatestBackup.Status == "UNHEALTHY" {
			veleroHealth.Status = "UNHEALTHY"
			veleroHealth.Message = fmt.Sprintf("Selected backup '%s' failed", backupName)
		} else if veleroHealth.LatestBackup.Status == "DEGRADED" {
			veleroHealth.Status = "DEGRADED"
			veleroHealth.Message = fmt.Sprintf("Selected backup '%s' has issues", backupName)
		} else {
			veleroHealth.Status = "HEALTHY"
			veleroHealth.Message = fmt.Sprintf("Selected backup '%s' is healthy", backupName)
		}
	}

	return veleroHealth
}

// checkVeleroRestores - Check and select specific restore
func checkVeleroRestores(kubeconfig string, veleroHealth VeleroHealth) VeleroHealth {
	fmt.Printf("\n     Checking for Velero restores...\n")

	cmd := exec.Command("kubectl", "get", "restores.velero.io", "-A", "--no-headers",
		"--sort-by=.metadata.creationTimestamp", "-o",
		"custom-columns=NAME:.metadata.name,PHASE:.status.phase,CREATED:.metadata.creationTimestamp")
	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	restoreOutput, err := cmd.Output()
	if err != nil {
		veleroHealth.Status = "HEALTHY"
		veleroHealth.Message = "Velero installed but no restores found or CRDs not available"
		return veleroHealth
	}

	restoreLines := strings.Split(strings.TrimSpace(string(restoreOutput)), "\n")
	if len(restoreLines) == 1 && strings.TrimSpace(restoreLines[0]) == "" {
		veleroHealth.Status = "HEALTHY"
		veleroHealth.Message = "Velero installed but no restores found"
		return veleroHealth
	}

	veleroHealth.BackupsFound = len(restoreLines)

	fmt.Printf("\n%s%s Available Restores:%s\n", ColorBold, ColorGreen, ColorReset)
	for i, line := range restoreLines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 3 {
			fmt.Printf("  %d. %s (Phase: %s, Created: %s)\n", i+1, fields[0], fields[1], fields[2])
		}
	}

	restoreChoice := promptForInput(fmt.Sprintf("\nSelect restore number (1-%d) or press Enter for latest: ", len(restoreLines)))

	var selectedLine string
	if restoreChoice == "" {
		selectedLine = strings.TrimSpace(restoreLines[len(restoreLines)-1])
	} else {
		choice, err := strconv.Atoi(restoreChoice)
		if err != nil || choice < 1 || choice > len(restoreLines) {
			veleroHealth.Status = "DEGRADED"
			veleroHealth.Message = "Invalid restore selection - using latest"
			selectedLine = strings.TrimSpace(restoreLines[len(restoreLines)-1])
		} else {
			selectedLine = strings.TrimSpace(restoreLines[choice-1])
		}
	}

	fields := strings.Fields(selectedLine)
	if len(fields) >= 3 {
		restoreName := fields[0]
		phase := fields[1]
		createdStr := fields[2]

		created, _ := time.Parse("2006-01-02T15:04:05Z", createdStr)

		fmt.Printf("\n     Selected restore: %s (Phase: %s)\n", restoreName, phase)

		detailFile := generateRestoreDetailFile(restoreName, kubeconfig)

		veleroHealth.LatestBackup = &VeleroBackup{
			Name:       restoreName,
			Status:     determineSimpleRestoreStatus(phase),
			Created:    created,
			DetailFile: detailFile,
		}

		if veleroHealth.LatestBackup.Status == "UNHEALTHY" {
			veleroHealth.Status = "UNHEALTHY"
			veleroHealth.Message = fmt.Sprintf("Selected restore '%s' failed", restoreName)
		} else if veleroHealth.LatestBackup.Status == "DEGRADED" {
			veleroHealth.Status = "DEGRADED"
			veleroHealth.Message = fmt.Sprintf("Selected restore '%s' has issues", restoreName)
		} else {
			veleroHealth.Status = "HEALTHY"
			veleroHealth.Message = fmt.Sprintf("Selected restore '%s' is healthy", restoreName)
		}
	}

	return veleroHealth
}

// generateBackupDetailFile - Create backup detail file
func generateBackupDetailFile(backupName, kubeconfig string) string {
	timestamp := time.Now().Format("20060102-150405")
	detailFile := fmt.Sprintf("velero-backup-details-%s-%s.txt", backupName, timestamp)

	cmd := exec.Command("velero", "backup", "describe", backupName, "--details", "--colorized=false")
	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	output, err := cmd.Output()
	if err == nil {
		if writeErr := os.WriteFile(detailFile, output, 0644); writeErr == nil {
			fmt.Printf("       ✓ Backup details saved to: %s\n", detailFile)
			return detailFile
		}
	}

	fmt.Printf("       Warning: Could not generate backup detail file: %v\n", err)
	return ""
}

// generateRestoreDetailFile - Create restore detail file
func generateRestoreDetailFile(restoreName, kubeconfig string) string {
	timestamp := time.Now().Format("20060102-150405")
	detailFile := fmt.Sprintf("velero-restore-details-%s-%s.txt", restoreName, timestamp)

	cmd := exec.Command("velero", "restore", "describe", restoreName, "--details", "--colorized=false")
	if kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	}

	output, err := cmd.Output()
	if err == nil {
		if writeErr := os.WriteFile(detailFile, output, 0644); writeErr == nil {
			fmt.Printf("       ✓ Restore details saved to: %s\n", detailFile)
			return detailFile
		}
	}

	fmt.Printf("       Warning: Could not generate restore detail file: %v\n", err)
	return ""
}

// determineSimpleBackupStatus - Simple backup status determination
func determineSimpleBackupStatus(phase string) string {
	switch strings.ToLower(phase) {
	case "completed":
		return "HEALTHY"
	case "partiallyfailed", "inprogress", "new":
		return "DEGRADED"
	case "failed", "failedvalidation":
		return "UNHEALTHY"
	default:
		return "UNHEALTHY"
	}
}

// determineSimpleRestoreStatus - Simple restore status determination
func determineSimpleRestoreStatus(phase string) string {
	switch strings.ToLower(phase) {
	case "completed":
		return "HEALTHY"
	case "partiallyfailed", "inprogress", "new":
		return "DEGRADED"
	case "failed", "failedvalidation":
		return "UNHEALTHY"
	default:
		return "UNHEALTHY"
	}
}

// generateLegacyRecommendations produces guidance for the original four checks.
func generateLegacyRecommendations(report HealthReport) []string {
	var recommendations []string

	if report.ReadinessCheck.Status == "UNHEALTHY" {
		recommendations = append(recommendations, "Investigate SAS readiness pod issues - check pod logs and events")
	} else if report.ReadinessCheck.Status == "DEGRADED" {
		recommendations = append(recommendations, "Monitor SAS readiness pod - consider investigating restart causes")
	}

	if report.CASCheck.Status == "UNHEALTHY" {
		recommendations = append(recommendations, "Critical CAS components are failing - immediate attention required")
		recommendations = append(recommendations, "Check CAS pod logs and Kubernetes events for error details")
	} else if report.CASCheck.Status == "DEGRADED" {
		recommendations = append(recommendations, "Monitor CAS components - some issues detected")
	}

	if report.PostgreSQLCheck.Status == "UNHEALTHY" {
		recommendations = append(recommendations, "Critical PostgreSQL clusters are failing - immediate attention required")
		recommendations = append(recommendations, "Check PostgreSQL pod logs and Kubernetes events for error details")
	} else if report.PostgreSQLCheck.Status == "DEGRADED" {
		recommendations = append(recommendations, "Monitor PostgreSQL clusters - some issues detected")
	}

	if report.VeleroCheck.Status == "UNHEALTHY" {
		recommendations = append(recommendations, "Critical Velero backup issues detected - immediate attention required")
		recommendations = append(recommendations, "Check Velero pod logs and Kubernetes events for error details")
	} else if report.VeleroCheck.Status == "DEGRADED" {
		recommendations = append(recommendations, "Monitor Velero backups - some issues detected")
	}

	return recommendations
}

// displayLegacyResults renders the readiness/CAS/PostgreSQL/Velero sections.
func displayLegacyResults(report HealthReport) {
	readinessColor := getStatusColor(report.ReadinessCheck.Status)
	readinessSymbol := getStatusSymbol(report.ReadinessCheck.Status)

	fmt.Printf("%s%s┌─ READINESS CHECK %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 61), ColorReset)
	fmt.Printf("│ %s Status:%s %s%s %s %s%s\n", ColorBold, ColorReset, readinessColor, readinessSymbol, report.ReadinessCheck.Status, ColorReset, readinessColor)
	if report.ReadinessCheck.PodInfo.Name != "" {
		fmt.Printf("│ %s Pod:%s    %s\n", ColorBold, ColorReset, report.ReadinessCheck.PodInfo.Name)
		fmt.Printf("│ %s Ready:%s  %s\n", ColorBold, ColorReset, report.ReadinessCheck.PodInfo.Ready)
		fmt.Printf("│ %s Phase:%s  %s\n", ColorBold, ColorReset, report.ReadinessCheck.PodInfo.Status)
		if report.ReadinessCheck.PodInfo.RestartCount != "" {
			fmt.Printf("│ %s Restarts:%s %s\n", ColorBold, ColorReset, report.ReadinessCheck.PodInfo.RestartCount)
		}
	}
	fmt.Printf("│ %s Message:%s %s\n", ColorBold, ColorReset, report.ReadinessCheck.Message)
	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)

	casColor := getStatusColor(report.CASCheck.Status)
	casSymbol := getStatusSymbol(report.CASCheck.Status)

	fmt.Printf("%s%s┌─ CAS SERVICES CHECK %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 57), ColorReset)
	fmt.Printf("│ %s Status:%s     %s%s %s %s%s\n", ColorBold, ColorReset, casColor, casSymbol, report.CASCheck.Status, ColorReset, casColor)
	fmt.Printf("│ %s Setup Type:%s %s\n", ColorBold, ColorReset, report.CASCheck.SetupType)
	fmt.Printf("│ %s Message:%s    %s\n", ColorBold, ColorReset, report.CASCheck.Message)
	fmt.Printf("│\n")
	fmt.Printf("│ %s Components:%s\n", ColorBold, ColorReset)

	if report.CASCheck.Components.Control.Name != "" {
		fmt.Printf("│   %s Control:%s    %s (%s, Ready: %s)\n",
			ColorBold, ColorReset,
			report.CASCheck.Components.Control.Name,
			report.CASCheck.Components.Control.Status,
			report.CASCheck.Components.Control.Ready)
	}

	if report.CASCheck.Components.Operator.Name != "" {
		fmt.Printf("│   %s Operator:%s   %s (%s, Ready: %s)\n",
			ColorBold, ColorReset,
			report.CASCheck.Components.Operator.Name,
			report.CASCheck.Components.Operator.Status,
			report.CASCheck.Components.Operator.Ready)
	}

	if report.CASCheck.Components.Server.Controller.Name != "" {
		fmt.Printf("│   %s Controller:%s %s (%s, Ready: %s)\n",
			ColorBold, ColorReset,
			report.CASCheck.Components.Server.Controller.Name,
			report.CASCheck.Components.Server.Controller.Status,
			report.CASCheck.Components.Server.Controller.Ready)
	}

	if report.CASCheck.Components.Server.Backup.Name != "" {
		fmt.Printf("│   %s Backup:%s     %s (%s, Ready: %s)\n",
			ColorBold, ColorReset,
			report.CASCheck.Components.Server.Backup.Name,
			report.CASCheck.Components.Server.Backup.Status,
			report.CASCheck.Components.Server.Backup.Ready)
	}

	if len(report.CASCheck.Components.Server.Workers) > 0 {
		fmt.Printf("│   %s Workers:%s\n", ColorBold, ColorReset)
		for _, worker := range report.CASCheck.Components.Server.Workers {
			fmt.Printf("│     • %s (%s, Ready: %s)\n", worker.Name, worker.Status, worker.Ready)
		}
	}

	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)

	pgColor := getStatusColor(report.PostgreSQLCheck.Status)
	pgSymbol := getStatusSymbol(report.PostgreSQLCheck.Status)

	fmt.Printf("%s%s┌─ POSTGRESQL CHECK %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 59), ColorReset)
	fmt.Printf("│ %s Status:%s       %s%s %s %s%s\n", ColorBold, ColorReset, pgColor, pgSymbol, report.PostgreSQLCheck.Status, ColorReset, pgColor)
	fmt.Printf("│ %s Clusters Found:%s %d\n", ColorBold, ColorReset, report.PostgreSQLCheck.ClustersFound)
	fmt.Printf("│ %s Message:%s      %s\n", ColorBold, ColorReset, report.PostgreSQLCheck.Message)

	if len(report.PostgreSQLCheck.Clusters) > 0 {
		fmt.Printf("│\n")
		fmt.Printf("│ %s Cluster Details:%s\n", ColorBold, ColorReset)

		for _, cluster := range report.PostgreSQLCheck.Clusters {
			clusterColor := getStatusColor(cluster.Status)
			clusterSymbol := getStatusSymbol(cluster.Status)

			fmt.Printf("│   %s%s%s %s%s%s (%d/%d pods healthy)\n",
				clusterColor, clusterSymbol, ColorReset,
				ColorBold, cluster.Name, ColorReset,
				cluster.HealthyPods, cluster.TotalPods)

			if cluster.Leader.Name != "" {
				roleDisplay := cluster.Leader.Role
				if cluster.Leader.IsLeader {
					roleDisplay = "Primary"
				}
				fmt.Printf("│     Leader:   %s (%s, Ready: %s, Role: %s)\n",
					cluster.Leader.Name, cluster.Leader.Status, cluster.Leader.Ready, roleDisplay)
			}

			connColor := getStatusColor(cluster.Connectivity.Status)
			connSymbol := getStatusSymbol(cluster.Connectivity.Status)
			fmt.Printf("│     Connect:  %s%s%s %s\n", connColor, connSymbol, ColorReset, cluster.Connectivity.Message)

			if len(cluster.Databases) > 0 {
				fmt.Printf("│     Databases (%d total):\n", len(cluster.Databases))
				var totalTables int
				for _, db := range cluster.Databases {
					totalTables += db.TableCount
					fmt.Printf("│       • %s: %s, %d tables\n", db.Name, db.Size, db.TableCount)
				}
				fmt.Printf("│       Total tables across all databases: %d\n", totalTables)
			}

			if cluster.RepoHost.Name != "" {
				fmt.Printf("│     Repo:     %s (%s, Ready: %s)\n", cluster.RepoHost.Name, cluster.RepoHost.Status, cluster.RepoHost.Ready)
			}

			if len(cluster.Replicas) > 1 {
				replicaCount := 0
				for _, replica := range cluster.Replicas {
					if replica.Name != cluster.Leader.Name {
						replicaCount++
					}
				}
				if replicaCount > 0 {
					fmt.Printf("│     Replicas: %d instances\n", replicaCount)
					for _, replica := range cluster.Replicas {
						if replica.Name != cluster.Leader.Name {
							fmt.Printf("│       • %s (%s, Ready: %s)\n", replica.Name, replica.Status, replica.Ready)
						}
					}
					fmt.Printf("│\n")
				}
			}

			fmt.Printf("│     Status:   %s\n", cluster.Message)
			fmt.Printf("│\n")
		}
	}

	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)

	veleroColor := getStatusColor(report.VeleroCheck.Status)
	veleroSymbol := getStatusSymbol(report.VeleroCheck.Status)

	fmt.Printf("%s%s┌─ VELERO CHECK %s%s\n", ColorBold, ColorWhite, strings.Repeat("─", 62), ColorReset)
	fmt.Printf("│ %s Status:%s        %s%s %s %s%s\n", ColorBold, ColorReset, veleroColor, veleroSymbol, report.VeleroCheck.Status, ColorReset, veleroColor)
	fmt.Printf("│ %s Installation:%s  %t\n", ColorBold, ColorReset, report.VeleroCheck.InstallationFound)
	fmt.Printf("│ %s Backups Found:%s %d\n", ColorBold, ColorReset, report.VeleroCheck.BackupsFound)
	fmt.Printf("│ %s Message:%s       %s\n", ColorBold, ColorReset, report.VeleroCheck.Message)

	if report.VeleroCheck.LatestBackup != nil {
		backup := report.VeleroCheck.LatestBackup
		backupColor := getStatusColor(backup.Status)
		backupSymbol := getStatusSymbol(backup.Status)

		fmt.Printf("│\n")
		fmt.Printf("│ %s Latest Backup:%s\n", ColorBold, ColorReset)
		fmt.Printf("│   %s%s%s %s%s%s\n", backupColor, backupSymbol, ColorReset, ColorBold, backup.Name, ColorReset)
		fmt.Printf("│     Created:          %s\n", backup.Created.Format("2006-01-02 15:04:05"))
		if backup.DetailFile != "" {
			fmt.Printf("│     Detail File:      %s\n", backup.DetailFile)
		}
		fmt.Printf("│     Status:           %s\n", backup.Status)
		fmt.Printf("│\n")
	}

	fmt.Printf("%s└%s%s\n\n", ColorWhite, strings.Repeat("─", 79), ColorReset)
}
