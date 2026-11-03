import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import tomllib


serve = Path(os.environ["serve"]).read_text()
config_path = re.search(r"-config (\S+)", serve).group(1)
proxy = json.loads(Path(config_path).read_text())["mcpServers"]["parallels"]
inventory = json.loads(Path(os.environ["inventory"]).read_text())["parallels"]
allowed = inventory["reads"] + inventory["confirms"]
assert proxy["transportType"] == "stdio"
assert proxy["command"] == os.environ["binary"]
assert proxy["args"] == ["--allow-tools", ",".join(allowed)]
assert proxy["options"]["toolFilter"] == {"mode": "allow", "list": allowed}
assert proxy["env"]["PDFM_MCP_TELEMETRY_DISABLED"] == "true"
assert "/usr/local/bin" in proxy["env"]["PATH"].split(":")
assert proxy["timeout"] == "6m"
assert "parallels" not in json.loads(os.environ["devboxEndpoints"])
agent_proxy = json.loads(Path(os.environ["agentConfig"]).read_text())["mcpProxy"]
assert agent_proxy["localServers"] == ["parallels"]
assert agent_proxy["endpoints"]["parallels"] == "/parallels/mcp"

with tempfile.TemporaryDirectory() as directory:
    home = Path(directory)
    (home / ".codex").mkdir()
    codex = home / ".codex/config.toml"
    claude = home / ".claude.json"
    codex.write_text('[mcp_servers.keep]\ncommand = "keep"\n'
                     '[mcp_servers.parallels.tools.parallels_vm_exec]\napproval_mode = "approve"\n')
    claude.write_text(json.dumps({"mcpServers": {"keep": {"command": "keep"}}}))
    env = dict(os.environ, HOME=directory)
    before = (codex.read_bytes(), claude.read_bytes())
    for script in ("codexActivation", "claudeActivation"):
        subprocess.run(["bash", "-e", os.environ[script]], env=dict(env, DRY_RUN="1"), check=True)
    assert before == (codex.read_bytes(), claude.read_bytes()), "dry run changed client files"
    for script in ("codexActivation", "claudeActivation"):
        subprocess.run(["bash", "-e", os.environ[script]], env=env, check=True)
    codex_servers = tomllib.loads(codex.read_text())["mcp_servers"]
    claude_servers = json.loads(claude.read_text())["mcpServers"]
    entry = codex_servers["parallels"]
    assert entry["default_tools_approval_mode"] == "prompt", "native approval is not prompt"
    assert "tools" not in entry, "stale per-tool approval survived registration"
    assert entry["tool_timeout_sec"] > 360
    assert entry["url"] == "http://127.0.0.1:6290/parallels/mcp"
    assert entry["http_headers_helper"]
    assert claude_servers["parallels"]["url"] == entry["url"]
    assert claude_servers["parallels"]["type"] == "http"
    assert claude_servers["parallels"]["headersHelper"] == entry["http_headers_helper"]
    assert codex_servers["keep"] == claude_servers["keep"] == {"command": "keep"}
    before = (codex.read_bytes(), claude.read_bytes())
    for script in ("codexActivation", "claudeActivation"):
        subprocess.run(["bash", "-e", os.environ[script]], env=env, check=True)
    assert before == (codex.read_bytes(), claude.read_bytes()), "registration is not idempotent"
print("Parallels proxy and client registration passed")
