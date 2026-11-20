"""Check the debugging guest's rendered provisioning and isolation contract."""

import copy
import json
from pathlib import Path
import subprocess
import sys


def check(template, config):
    scripts = {p["mode"]: p["script"] for p in template["provision"]}
    system = scripts["system"]
    for package in ("strace", "bpftrace", "gdb", "delve", "lldb", "linux-tools-common"):
        assert package in system.split(), f"debug provisioning lacks {package}"
    assert '"linux-tools-$(uname -r)"' in system, "perf must match the running kernel"
    assert "%sudo ALL=(ALL:ALL) NOPASSWD: ALL" in system
    assert "visudo -cf /etc/sudoers.d/90-agent-debug" in system
    assert "mount -t tracefs tracefs /sys/kernel/tracing" in system
    assert "test -r /sys/kernel/btf/vmlinux" in system
    assert "perf version" in system
    assert "sudo -n true" in scripts["user"]
    assert config["instance"] == "agent-debug"
    assert config["workdir"] == "src/project"
    assert config["limaHome"].endswith("/lima-debug/lima")
    assert config["outbox"].endswith("/lima-debug-outbox")
    assert not config.get("quic") and not config.get("shim")
    assert config["egress"]["quicServers"] == []
    assert config["privateDialEndpoints"] == []
    assert template["mounts"] == [] and template["networks"] == []
    assert template["plain"] is True
    assert template["ssh"] == {"loadDotSSHPubKeys": False, "forwardAgent": False, "forwardX11": False}
    assert template["hostResolver"] == {"enabled": False}
    assert template["portForwards"] == [{"guestIP": "0.0.0.0", "guestPortRange": [1, 65535], "proto": "any", "ignore": True}]
    assert "caCerts" not in template
    assert "ngrok-agent" not in system and "lima-udp-shim" not in system
    for script in scripts.values():
        subprocess.run(["sh", "-n"], input=script, text=True, check=True)


template = json.loads(Path(sys.argv[1]).read_text())
config = json.loads(Path(sys.argv[2]).read_text())
check(template, config)

# Prove the check rejects both a missing tool and a host-sharing regression.
for violation in ("package", "mount"):
    broken = copy.deepcopy(template)
    if violation == "package":
        broken["provision"][0]["script"] = broken["provision"][0]["script"].replace(" strace ", " ")
    else:
        broken["mounts"] = [{"location": "~", "writable": True}]
    try:
        check(broken, config)
    except AssertionError:
        pass
    else:
        raise AssertionError(f"accepted deliberate {violation} violation")
print("Debug guest provisioning and isolation passed; deliberate violations rejected")
