"""The managed Lima VM's rendered template and configuration.

flake.nix's lima-dev-template check runs this with the ngrok Mac's
template, configuration and pinned limactl. It asserts what the VM's
isolation rests on, so that a change to the template or its options that
loosens one fails here: no mounts, networks, forwards or host resolver,
plain mode, the broker CA at its fixed path, a pinned image, the guest's
proxy and private-dial names pointing only at the relays, a guard
beside the store firmware, and an import's store git and outbox outside
the Lima home.
"""

import json
import os
from pathlib import Path
import subprocess

import yaml

template_path = os.environ["template"]
config = json.loads(Path(os.environ["config"]).read_text())
template = yaml.safe_load(Path(template_path).read_text())

# Lima itself accepts the template.
subprocess.run([os.environ["limactl"], "validate", template_path], check=True,
               env={"HOME": os.environ["TMPDIR"], "PATH": "/usr/bin:/bin:/usr/sbin:/sbin"})

# Every top-level key is one reviewed here: a new one (additionalDisks,
# env, firmware, rosetta, ...) fails until it is.
assert set(template) == {
    "vmType", "arch", "plain", "cpus", "memory", "disk", "images", "mounts", "networks", "ssh",
    "hostResolver", "portForwards", "propagateProxyEnv", "containerd", "upgradePackages", "video",
    "caCerts", "provision",
}, sorted(template)
assert template["vmType"] == "qemu" and template["arch"] == "aarch64"
assert template["plain"] is True
assert template["mounts"] == [] and template["networks"] == []
assert template["ssh"] == {"loadDotSSHPubKeys": False, "forwardAgent": False, "forwardX11": False}
assert template["hostResolver"] == {"enabled": False}
assert template["portForwards"] == [{"guestIP": "0.0.0.0", "guestPortRange": [1, 65535], "proto": "any", "ignore": True}]
assert template["propagateProxyEnv"] is False
assert template["containerd"] == {"system": False, "user": False}
assert template["video"] == {"display": "none"}
assert template["caCerts"] == {"files": [config["limaHome"] + "/_mcp/ca.pem"]}
for image in template["images"]:
    assert image["location"].startswith("https://"), image
    assert image["digest"].startswith("sha256:") and len(image["digest"]) == 71, image

system = [p["script"] for p in template["provision"] if p["mode"] == "system"]
assert len(system) == 1 and {p["mode"] for p in template["provision"]} <= {"system", "user"}
script = system[0]
proxy = "http://" + config["connect"]
assert f"https_proxy={proxy}\n" in script and f'Acquire::https::Proxy "{proxy}";' in script
# Each private-dial server resolves to the guest's shim, and the shim
# reaches only the QUIC relay.
servers = config["egress"]["quicServers"]
assert servers
for server in servers:
    assert f"echo '127.0.1.1 {server}'" in script, server
assert f"-listen 127.0.1.1:443 -connect {config['quic']}\n" in script
assert '"private_endpoint_server_transport":"h3"' in script
assert '"private_endpoint_server_url":"us.h2.connect-endpoint.ngrok.com:443"' in script

# The relays' addresses are the ones the guard forwards.
assert config["connect"] == "192.168.5.100:3128" and config["quic"] == "192.168.5.101:443"
assert config["template"] == template_path
# Guest commands run in the configured workdir, which provisioning makes.
user = [p["script"] for p in template["provision"] if p["mode"] == "user"]
assert len(user) == 1 and f'mkdir -p "$HOME/{config["workdir"]}"' in user[0], user
assert not any(
    host.removeprefix("*.") == "connect-endpoint.ngrok.com"
    or host.endswith(".connect-endpoint.ngrok.com")
    for host in config["egress"]["allow"]
)

# An import checks bundles with the store's git, from an outbox outside
# the Lima home, which sessions may not read.
assert config["git"].startswith("/nix/store/") and Path(config["git"]).name == "git", config["git"]
assert Path(config["git"]).is_file(), config["git"]
outbox = config["outbox"]
assert outbox.startswith(config["home"] + "/") and os.path.normpath(outbox) == outbox, outbox
assert not (outbox + "/").startswith(config["limaHome"] + "/"), outbox

# Lima finds the firmware beside the guard, and the guard runs lima-mcp's
# qemu-guard with its own configuration from the store, never one in the
# Lima home, which QEMU and lima-mcp write.
guard = Path(config["guard"])
assert guard.name == "qemu-system-aarch64"
assert (guard.parent.parent / "share/qemu/edk2-aarch64-code.fd").is_file()
guard_config_path = os.environ["guardConfig"]
assert guard_config_path.startswith("/nix/store/")
assert f"qemu-guard --config {guard_config_path} --".encode() in guard.read_bytes()
assert b"_mcp/config.json" not in guard.read_bytes()
# The guard's configuration is the configuration's guard part, naming no
# guard.
guard_config = json.loads(Path(guard_config_path).read_text())
assert set(guard_config) == {"home", "limaHome", "instance", "qemu", "deniedReads", "connect", "quic"}, sorted(guard_config)
assert all(config[k] == v for k, v in guard_config.items())
assert guard_config["qemu"].startswith("/nix/store/") and guard_config["qemu"].endswith("/bin/qemu-system-aarch64")
# The shim is a Linux arm64 executable: ELF, machine 183 (AArch64).
shim = Path(config["shim"]).read_bytes()[:20]
assert shim[:4] == b"\x7fELF" and int.from_bytes(shim[18:20], "little") == 183, shim
print("Lima template and configuration passed")
