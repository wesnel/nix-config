"""Exercise the generated MCP registration in an isolated Codex home."""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tomllib


EXPECTED = {
    "agent_plan_" + name
    for name in (
        "new log set unfile ask decide followup step part edit after section "
        "reference link postpone sign_id workstream_new workstream_list find status"
    ).split()
}


def check(config):
    server = config["mcp_servers"]["agent_ops"]
    assert server["default_tools_approval_mode"] == "prompt", server
    assert set(server["tools"]) == EXPECTED, server["tools"]
    assert all(value == {"approval_mode": "approve"} for value in server["tools"].values())


def main():
    activation = Path(sys.argv[1]).read_text()
    with tempfile.TemporaryDirectory() as directory:
        home = Path(directory)
        config_file = home / ".codex/config.toml"
        config_file.parent.mkdir()
        original = 'model = "keep-me"\n[mcp_servers.agent_ops]\ndefault_tools_approval_mode = "approve"\n'
        config_file.write_text(original)
        env = dict(os.environ, HOME=directory, DRY_RUN="1")
        subprocess.run(["bash", "-eu", "-c", activation], env=env, check=True)
        assert config_file.read_text() == original
        env["DRY_RUN"] = ""
        for _ in range(2):
            subprocess.run(["bash", "-eu", "-c", activation], env=env, check=True)
            config = tomllib.loads(config_file.read_text())
            assert config["model"] == "keep-me"
            check(config)
        config["mcp_servers"]["agent_ops"]["tools"]["agent_pr_open"] = {"approval_mode": "approve"}
        try:
            check(config)
        except AssertionError:
            pass
        else:
            raise AssertionError("approval check accepted publishing without a prompt")
    print("MCP registration: exact approvals, preserved settings, repeat and dry run passed")


if __name__ == "__main__":
    main()
