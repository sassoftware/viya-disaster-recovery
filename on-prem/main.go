package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	configPath := flag.String("config", "environment.properties", "Path to environment.properties")
	steps := flag.String("steps", "", "Comma-separated setup steps: hpos,kubernetes,velero")
	backup := flag.Bool("backup", false, "Create Velero backup")
	restore := flag.Bool("restore", false, "Create Velero restore")
	destroy := flag.Bool("destroy", false, "Destroy HPOS teardown resources (Velero and libreFS bucket)")
	cleanup := flag.Bool("cleanup", false, "Alias for --destroy")
	check := flag.Bool("check", false, "Check Kubernetes connectivity")
	debug := flag.Bool("debug", false, "Enable debug command logging")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	fatalIf(err)
	r := Runner{Debug: *debug}
	fatalIf(cfg.ValidateForSetup())

	if *check {
		fatalIf(checkKubernetesConnectivity(cfg, r))
	}
	if *steps != "" {
		for _, step := range strings.Split(*steps, ",") {
			step = strings.TrimSpace(strings.ToLower(step))
			if step == "" {
				continue
			}
			fmt.Printf("\n==> Running step: %s\n", step)
			s := LoadState()
			s.Mark(step, "started")
			switch step {
			case "hpos", "librefs":
				fatalIf(setupHPOS(cfg, r))
			case "kubernetes":
				fatalIf(setupKubernetes(cfg, r))
			case "velero":
				fatalIf(setupVelero(cfg, r))
			default:
				fatalIf(fmt.Errorf("unknown step %q; valid steps: hpos,kubernetes,velero", step))
			}
			s = LoadState()
			s.Mark(step, "completed")
		}
	}
	if *backup {
		fatalIf(createBackup(cfg, r))
	}
	if *restore {
		fatalIf(createRestore(cfg, r))
	}
	if *destroy || *cleanup {
		s := LoadState()
		s.Mark("destroy", "started")
		err = cleanupDestroyResources(cfg, r)
		s = LoadState()
		if err != nil {
			s.Mark("destroy", "partial")
			fatalIf(err)
		}
		s.Mark("destroy", "completed")
	}
	if *steps == "" && !*backup && !*restore && !*check && !*destroy && !*cleanup {
		fmt.Println("Usage:")
		fmt.Println("  ./viya-dr-automation-hpos --steps=hpos,kubernetes,velero")
		fmt.Println("  ./viya-dr-automation-hpos --backup")
		fmt.Println("  ./viya-dr-automation-hpos --restore")
		fmt.Println("  ./viya-dr-automation-hpos --destroy")
		fmt.Println("  ./viya-dr-automation-hpos --check")
	}
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}
