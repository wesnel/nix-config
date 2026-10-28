package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/wesnel/agent-tools/internal/availability"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

func dashboardAdmission() error {
	_, err := os.Lstat(filepath.Join(hostpaths.StateHome(), "agent-plans", "off"))
	if err == nil {
		return errors.New("dashboard is explicitly off")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot read dashboard off state: %w", err)
	}
	state, err := availability.Check(availability.Claude, time.Now())
	if err != nil {
		return fmt.Errorf("cannot read Claude availability: %w", err)
	}
	if state.Reason != "" {
		return errors.New(state.String())
	}
	return nil
}

// The hook runs inside the worker cgroup, so it queues a stop without waiting
// for its own termination. The unit's KillMode and TimeoutStopSec bound cleanup.
func stopDashboard() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "systemctl", "--user", "--no-block", "stop", "agent-plans.service").Run(); err != nil {
		return fmt.Errorf("cannot request dashboard stop: %w", err)
	}
	return nil
}

func quotaLifecycleCommand(command string, args []string, stdin io.Reader, stderr io.Writer) int {
	flags := flag.NewFlagSet("agent-plan "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var stop bool
	if command == "quota-failure" {
		flags.BoolVar(&stop, "stop-dashboard", false, "")
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	report := func(err error) int {
		if err != nil {
			fmt.Fprintf(stderr, "agent-plan %s: %v\n", command, err)
			return 1
		}
		return 0
	}
	switch command {
	case "quota-admit":
		return report(dashboardAdmission())
	case "quota-control":
		if err := dashboardAdmission(); err != nil {
			fmt.Fprintf(stderr, "agent-plan quota-control: stopping dashboard: %v\n", err)
			return report(stopDashboard())
		}
		return 0
	case "quota-failure":
		var event struct {
			Name  string `json:"hook_event_name"`
			Error string `json:"error"`
		}
		input, err := io.ReadAll(io.LimitReader(stdin, (1<<20)+1))
		if err != nil || len(input) > 1<<20 || json.Unmarshal(input, &event) != nil || event.Name != "StopFailure" || event.Error == "" {
			return report(errors.New("invalid StopFailure event"))
		}
		if event.Error != "rate_limit" {
			return 0
		}
		holdErr := availability.Hold(availability.Claude, availability.State{Reason: availability.RateLimit})
		var stopErr error
		if stop {
			stopErr = stopDashboard()
		}
		return report(errors.Join(holdErr, stopErr))
	}
	return 2
}
