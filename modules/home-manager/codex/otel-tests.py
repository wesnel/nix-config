"""Exercise the generated [otel] step in an isolated Codex home.

Takes the activation script and the collector's URL.
"""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tomllib


def main():
    activation = Path(sys.argv[1]).read_text()
    url = sys.argv[2]
    # Written out here, not read from the module, so a changed key fails.
    expected = {
        "exporter": "none",
        "trace_exporter": "none",
        "log_user_prompt": False,
        "log_agent_responses": False,
        "metrics_exporter": {"otlp-http": {"endpoint": f"{url}/v1/metrics", "protocol": "binary"}},
    }
    with tempfile.TemporaryDirectory() as directory:
        config_file = Path(directory) / ".codex/config.toml"
        config_file.parent.mkdir()
        # A stale true from an earlier config must be overwritten.
        original = 'model = "keep-me"\n\n[otel]\nlog_agent_responses = true\nenvironment = "keep-me"\n'
        config_file.write_text(original)
        env = dict(os.environ, HOME=directory, DRY_RUN="1")
        subprocess.run(["bash", "-eu", "-c", activation], env=env, check=True)
        assert config_file.read_text() == original
        env["DRY_RUN"] = ""
        for _ in range(2):
            subprocess.run(["bash", "-eu", "-c", activation], env=env, check=True)
            config = tomllib.loads(config_file.read_text())
            otel = config["otel"]
            for key, value in expected.items():
                assert otel.get(key) == value, (key, otel.get(key))
            assert otel["environment"] == "keep-me", otel
            assert config["model"] == "keep-me", config
    print("Codex [otel]: metrics only to the collector, logs, traces and content off, preserved settings, repeat and dry run passed")


if __name__ == "__main__":
    main()
