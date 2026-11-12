package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/guard"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/repos"
	"github.com/wesnel/agent-tools/internal/space"
)

func readMirrorConfig(value any) error {
	data, err := os.ReadFile(repos.Path())
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func mirrorSession() (string, error) {
	harness, id := plans.EnvSession()
	if id == "" {
		return "", errors.New("no session identity: run the mirror from its owning Claude or Codex session")
	}
	return harness + ":" + id, nil
}

func sessionPlan(c plans.Config, session string) (string, error) {
	found, err := c.Find(plans.FindOptions{Session: session})
	if err != nil {
		return "", err
	}
	unique := map[string]bool{}
	for _, f := range found {
		unique[f.Plan] = true
	}
	if len(unique) != 1 {
		return "", fmt.Errorf("session matches %d plans: pass the current plan file explicitly", len(unique))
	}
	for file := range unique {
		return file, nil
	}
	return "", errors.New("no session plan")
}

func snapshot(args []string, stdout, stderr io.Writer) int {
	session, err := mirrorSession()
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	c, err := plans.Load()
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	file := ""
	all := false
	for _, arg := range args {
		if arg == "--open" {
			all = true
		} else {
			file = arg
		}
	}
	if len(args) == 0 {
		file, err = sessionPlan(c, session)
	}
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	html, err := export.Mirror(c, file, time.Now())
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	var result agentplans.Mirror
	if all {
		result, err = agentplans.PrepareAggregate(session, export.MirrorRevision(html), html)
	} else {
		p, openErr := c.Open(file)
		if openErr != nil {
			return cli.Report("plan-dash", nil, openErr, 0, stdout, stderr)
		}
		result, err = agentplans.PrepareMirror(session, p.File(), "Current plan", export.MirrorRevision(html), html)
	}
	return cli.Report("plan-dash", result, err, 0, stdout, stderr)
}

func mirrorClaim(release bool, stdout, stderr io.Writer) int {
	session, err := mirrorSession()
	status := "released"
	if err == nil {
		if release {
			err = agentplans.ReleaseAggregate(session)
		} else {
			status, err = agentplans.ClaimAggregate(session)
		}
	}
	targets, targetErr := agentplans.LoadTargets()
	err = errors.Join(err, targetErr)
	return cli.Report("plan-dash", struct {
		Status string `json:"status"`
		Page   string `json:"page"`
		Owner  string `json:"owner"`
	}{status, targets.ClaudePage, session}, err, 0, stdout, stderr)
}

func mirrorRecord(stdin io.Reader, stdout, stderr io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(stdin, 2<<20))
	if err == nil {
		err = guard.RecordMirror(data)
	}
	return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
}

// A reminder runs only on a user turn or session start; publication never
// runs in a hook and cannot recursively cause another model turn.
func mirrorRemind(stdin io.Reader, stdout io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return 0
	}
	var event struct {
		Event   string `json:"hook_event_name"`
		Session string `json:"session_id"`
		Agent   string `json:"agent_id"`
	}
	if json.Unmarshal(data, &event) != nil || event.Agent != "" || event.Event != "SessionStart" && event.Event != "UserPromptSubmit" {
		return 0
	}
	var config struct {
		AgentPlans struct {
			Enable bool `json:"enable"`
		} `json:"agentPlans"`
	}
	// The target config and reminder enablement share the generated config.
	if readMirrorConfig(&config) != nil || !config.AgentPlans.Enable {
		return 0
	}
	c, err := plans.Load()
	if err != nil {
		return 0
	}
	session, err := mirrorSession()
	if err != nil || event.Session == "" || !strings.HasSuffix(session, ":"+event.Session) {
		return 0
	}
	_, err = sessionPlan(c, session)
	if err != nil {
		return 0
	}
	const notice = "Keep the current plan mirror updated at this turn's checkpoints using the plan-mirrors skill. Plan files remain authoritative; publication is best effort and a refusal must not be bypassed."
	_, _ = stdout.Write(plans.WithContext(nil, data, notice))
	return 0
}

func refresh(stdout, stderr io.Writer) int {
	targets, err := agentplans.LoadTargets()
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	if targets.SpacePage == "" {
		return cli.Report("plan-dash", space.Result{Status: "unconfigured"}, nil, 0, stdout, stderr)
	}
	if err := os.MkdirAll(agentplans.Dir(), 0o700); err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	lock, err := os.OpenFile(filepath.Join(agentplans.Dir(), "refresh.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return cli.Report("plan-dash", space.Result{Status: "busy"}, nil, 0, stdout, stderr)
		}
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	c, err := plans.Load()
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	html, err := export.Mirror(c, "", time.Now())
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	p := space.Publisher{Dial: space.Codex(agentplans.Dir()), StateFile: agentplans.SpaceStateFile(), Dir: agentplans.Dir(), Revision: export.MirrorRevision(html)}
	result, err := p.Publish(ctx, targets.SpacePage, html)
	return cli.Report("plan-dash", result, err, 0, stdout, stderr)
}
