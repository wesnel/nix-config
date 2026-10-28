{
  writers,
  python3Packages,
}:
writers.writePython3Bin "codex-git-setup" {
  libraries = [python3Packages.tomlkit];
} ''
  import runpy

  runpy.run_path(
      "${./.}/setup_git.py",
      run_name="__main__",
  )
''
