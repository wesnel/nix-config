"""Exercise the generated footer status line step in an isolated Codex home."""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tomllib


# Written out here, not read from the module, so dropping an item fails.
EXPECTED = ["model-with-reasoning", "current-dir", "context-used", "five-hour-limit", "weekly-limit"]


def main():
    activation = Path(sys.argv[1]).read_text()
    with tempfile.TemporaryDirectory() as directory:
        config_file = Path(directory) / ".codex/config.toml"
        config_file.parent.mkdir()
        original = 'model = "keep-me"\n\n[tui]\ntheme = "dark"\n\n[tui.notifications]\nenabled = true\n'
        config_file.write_text(original)
        env = dict(os.environ, HOME=directory, DRY_RUN="1")
        subprocess.run(["bash", "-eu", "-c", activation], env=env, check=True)
        assert config_file.read_text() == original
        env["DRY_RUN"] = ""
        for _ in range(2):
            subprocess.run(["bash", "-eu", "-c", activation], env=env, check=True)
            config = tomllib.loads(config_file.read_text())
            assert config["tui"].get("status_line") == EXPECTED, config["tui"].get("status_line")
            assert config["model"] == "keep-me", config
            assert config["tui"]["theme"] == "dark", config
            assert config["tui"]["notifications"] == {"enabled": True}, config
    print("Footer status line: exact items, preserved settings, repeat and dry run passed")


if __name__ == "__main__":
    main()
