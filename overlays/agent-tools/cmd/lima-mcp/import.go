package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/limaimport"
	"github.com/wesnel/agent-tools/internal/limavm"
)

// importSeconds bounds the guest's part of an import, which runs under the
// guest's timeout; the host's checks and copy have the rest of
// importTimeout.
const (
	importSeconds = 600
	importTimeout = 15 * time.Minute
)

// importBundle carries the bundle name from the outbox into the guest's
// workdir as job id: limaimport.Check takes a private copy and checks it,
// before the VM is asked anything; then, holding the workdir alone and
// once the VM attests, the copy goes into the guest user's home and
// limaimport.GuestScript applies it there, as the job's guest command,
// which must leave the guest at the bundle's HEAD.
func (m *managed) importBundle(ctx context.Context, id string, name limaimport.Name) (string, bool, *int, error) {
	if err := os.MkdirAll(m.cfg.Imports(), 0o700); err != nil {
		return "", false, nil, err
	}
	b, err := limaimport.Check(ctx, m.cfg.Git, m.cfg.Outbox, name, m.cfg.Imports())
	if err != nil {
		return "", false, nil, err
	}
	defer b.Close()
	if !m.workdir.TryLock() {
		return "", false, nil, errors.New("another import or a command is running in the guest: import once its job ends")
	}
	defer m.workdir.Unlock()
	a, err := m.attest(ctx)
	if err != nil {
		return "", false, nil, err
	}
	if a.State != limavm.StateAttested {
		return "", false, nil, fmt.Errorf("the managed VM is %s: %s", a.State, a.Reason)
	}
	if err := m.refreshLinks(); err != nil {
		return "", false, nil, err
	}
	home := a.Listed.Config.User.Home
	if home == "" {
		return "", false, nil, errors.New("Lima reports no guest home")
	}
	guestBundle := home + "/.lima-import-" + id + ".bundle"
	if out, _, err := m.limactl(ctx, "copy", b.Path, m.cfg.Instance+":"+guestBundle); err != nil {
		return out, false, nil, fmt.Errorf("limactl copy: %w", err)
	}
	script := "set -- " + shellQuote(home+"/"+m.cfg.Workdir) + " " + shellQuote(guestBundle) + "\n" + limaimport.GuestScript
	out, truncated, code, err := m.guestRun(ctx, id, home, importSeconds, script)
	if err != nil {
		return out, truncated, code, err
	}
	// The script's last line is the guest's HEAD after the import.
	words := strings.Fields(out)
	if len(words) == 0 || words[len(words)-1] != b.Head {
		return out, truncated, code, fmt.Errorf("the guest's HEAD after the import is not the bundle's %s", b.Head)
	}
	return out, truncated, code, nil
}

// shellQuote is s as one word of a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func callImport(ctx context.Context, s *server, raw json.RawMessage) map[string]any {
	m, fail := s.managedVM()
	if fail != nil {
		return fail
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := strict(raw, &input); err != nil {
		return failure("invalid arguments: name is required")
	}
	name, err := limaimport.ParseName(input.Name)
	if err != nil {
		return failure("invalid arguments: " + err.Error())
	}
	j := m.jobs.start(importTimeout, func(ctx context.Context, id string) (string, bool, *int, error) {
		return m.importBundle(ctx, id, name)
	})
	return jsonText(map[string]any{"job": j.id})
}
