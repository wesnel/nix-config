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
lima = json.loads(Path(config_path).read_text())["mcpServers"]["lima"]
assert lima["transportType"] == "stdio"
assert lima["command"].endswith("/bin/lima-mcp")
assert lima["options"]["toolFilter"] == {"mode": "allow", "list": ["lima_list", "lima_stop"]}
requests = [
    {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test", "version": "1"}}},
    {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
]
result = subprocess.run([lima["command"]], input="".join(json.dumps(x) + "\n" for x in requests),
                        text=True, capture_output=True, check=True)
replies = [json.loads(line) for line in result.stdout.splitlines()]
assert replies[0]["result"]["protocolVersion"] == "2025-06-18"
assert [tool["name"] for tool in replies[1]["result"]["tools"]] == lima["options"]["toolFilter"]["list"]
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
assert "lima" not in json.loads(os.environ["devboxEndpoints"])
agent_proxy = json.loads(Path(os.environ["agentConfig"]).read_text())["mcpProxy"]
assert agent_proxy["localServers"] == ["lima", "parallels"]
assert agent_proxy["endpoints"]["parallels"] == "/parallels/mcp"

with tempfile.TemporaryDirectory() as directory:
    home = Path(directory)
    (home / ".codex").mkdir()
    codex = home / ".codex/config.toml"
    claude = home / ".claude.json"
    codex.write_text('[mcp_servers.keep]\ncommand = "keep"\n'
                     '[mcp_servers.parallels.tools.parallels_vm_exec]\napproval_mode = "approve"\n'
                     '[mcp_servers.lima.tools.lima_exec]\napproval_mode = "approve"\n')
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
    assert codex_servers["lima"]["default_tools_approval_mode"] == "prompt"
    assert "tools" not in codex_servers["lima"], "stale Lima approval survived registration"
    assert codex_servers["lima"]["url"] == "http://127.0.0.1:6290/lima/mcp"
    assert codex_servers["lima"]["tool_timeout_sec"] > 300
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
