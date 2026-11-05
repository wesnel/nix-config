package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/wesnel/agent-tools/internal/limavm"
)

func TestLimaDev(t *testing.T) {
	v := newVM(t)
	var argv, env []string
	execute := func(path string, a, e []string) error {
		argv, env = append([]string{path}, a...), e
		return nil
	}
	if err := devMain([]string{"--config", v.configFile, "--", "start", "ngrok-agent"}, execute); err != nil {
		t.Fatal(err)
	}
	if want := []string{v.cfg.Limactl, v.cfg.Limactl, "start", "ngrok-agent"}; !slices.Equal(argv, want) {
		t.Errorf("executed %q; want %q", argv, want)
	}
	if !slices.Contains(env, "QEMU_SYSTEM_AARCH64="+v.cfg.Guard) || !slices.Contains(env, "LIMA_HOME="+v.cfg.LimaHome) || len(env) != 5 {
		t.Errorf("environment %q", env)
	}
	// The same links and CA as lima-mcp's start, so that a VM the
	// operator starts gets the relays and their policy.
	for link, want := range map[string]string{v.cfg.ConfigLink(): v.configFile, v.cfg.BinLink(): v.cfg.LimaMCP} {
		if got, err := os.Readlink(link); err != nil || got != want {
			t.Errorf("link %s -> %q, %v; want %s", link, got, err, want)
		}
	}
	if _, err := limavm.LoadCA(v.cfg.CACert(), v.cfg.CAKey()); err != nil {
		t.Error(err)
	}
}

func TestLimaDevRefusesStart(t *testing.T) {
	for name, setup := range map[string]func(v *vm){
		"while a Lima override is configured": func(v *vm) {
			os.MkdirAll(filepath.Join(v.cfg.LimaHome, "_config"), 0o700)
			os.WriteFile(filepath.Join(v.cfg.LimaHome, "_config", "override.yaml"), nil, 0o600)
		},
		"of an instance not made from the template": func(v *vm) {
			os.MkdirAll(v.cfg.InstanceDir(), 0o700)
			os.WriteFile(filepath.Join(v.cfg.InstanceDir(), "lima.yaml"), []byte("vmType: vz\n"), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := newVM(t)
			setup(v)
			run := func(limactl ...string) (bool, error) {
				executed := false
				execute := func(string, []string, []string) error { executed = true; return nil }
				err := devMain(append([]string{"--config", v.configFile, "--"}, limactl...), execute)
				return executed, err
			}
			// Every command that may start the VM, however spelled.
			for _, limactl := range [][]string{
				{"start", "ngrok-agent"},
				{"restart", "ngrok-agent"},
				{"create", "--start", "--name", "ngrok-agent"},
				{"--log-level", "debug", "start", "ngrok-agent"},
			} {
				if executed, err := run(limactl...); err == nil || executed {
					t.Errorf("%q: err %v, executed %v; want it refused", limactl, err, executed)
				}
			}
			// The operator may still inspect, stop and delete it.
			for _, limactl := range [][]string{
				{"list"}, {"stop", "-f", "ngrok-agent"}, {"delete", "ngrok-agent"}, {"info"}, {"--version"}, {"help", "start"},
			} {
				if executed, err := run(limactl...); err != nil || !executed {
					t.Errorf("%q: err %v, executed %v; want it run", limactl, err, executed)
				}
			}
		})
	}
}
