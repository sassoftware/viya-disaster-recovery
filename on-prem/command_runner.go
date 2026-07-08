package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Runner struct {
	Debug bool
}

func (r Runner) Run(name string, args ...string) error {
	_, err := r.Output(name, args...)
	return err
}

func (r Runner) Output(name string, args ...string) (string, error) {
	if r.Debug {
		fmt.Printf("+ %s %s\n", name, strings.Join(args, " "))
	}
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("command failed: %s %s\nstdout:\n%s\nstderr:\n%s\nerror: %w", name, strings.Join(args, " "), stdout.String(), stderr.String(), err)
	}
	if out := stdout.String(); strings.TrimSpace(out) != "" {
		fmt.Print(out)
	}
	return stdout.String(), nil
}

func (r Runner) Shell(script string) error {
	return r.Run("bash", "-lc", script)
}
