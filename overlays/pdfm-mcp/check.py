import json
from pathlib import Path
import selectors
import subprocess
import sys
import tempfile


def tools(binary, args):
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        # Real CLIs could initialize Parallels or inspect the operator's VMs.
        for name in ("prlctl", "prlsrvctl"):
            cli = root / name
            cli.write_text(
                '#!/bin/sh\nprintf "%s\\n" "$*" >> "$PDFM_TEST_CALLS"\n'
                'case "$1" in list) printf "[]\\n" ;; *) exit 64 ;; esac\n'
            )
            cli.chmod(0o755)
        env = {
            "HOME": directory,
            "TMPDIR": directory,
            "PATH": directory + ":/usr/bin:/bin",
            "PDFM_MCP_TELEMETRY_DISABLED": "true",
            "PDFM_MCP_LOG_LEVEL": "error",
            "PDFM_TEST_CALLS": str(root / "calls"),
        }
        process = subprocess.Popen(
            [binary, *args], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, text=True, env=env,
        )
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)

            def request(message):
                process.stdin.write(json.dumps(message) + "\n")
                process.stdin.flush()
                if "id" not in message:
                    return
                if not selector.select(timeout=15):
                    raise RuntimeError("MCP response timed out")
                line = process.stdout.readline()
                if not line:
                    raise RuntimeError("MCP server exited before its response")
                response = json.loads(line)
                assert response.get("id") == message["id"], response
                assert "error" not in response, response
                return response["result"]

            try:
                result = request({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
                    "protocolVersion": "2024-11-05", "capabilities": {},
                    "clientInfo": {"name": "package-check", "version": "1"},
                }})
                assert result["serverInfo"]["name"] == "pdfm-mcp", result
                request({"jsonrpc": "2.0", "method": "notifications/initialized"})
                result = request({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
                names = {tool["name"] for tool in result["tools"]}
            finally:
                process.terminate()
                process.communicate(timeout=5)
            calls = root / "calls"
            if calls.exists():
                assert all(line.startswith("list ") for line in calls.read_text().splitlines()), calls.read_text()
            return names


binary, inventory_path, recorded_path = sys.argv[1:]
inventory = json.loads(Path(inventory_path).read_text())["parallels"]
recorded = {tool["name"] for tool in json.loads(Path(recorded_path).read_text())["tools"]}
actual = tools(binary, [])
assert actual == recorded, f"upstream tool inventory changed: {actual ^ recorded}"
allowed = set(inventory["reads"] + inventory["confirms"])
assert "parallels_vm_exec" in allowed and "parallels_vm_delete" not in allowed
actual = tools(binary, ["--allow-tools", ",".join(sorted(allowed))])
assert actual == allowed, f"upstream allow-tools mismatch: {actual ^ allowed}"
print("Parallels MCP handshake, tool inventory and allowlist passed")
