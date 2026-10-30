# A program that reads whether the SKILL.md it is given keeps Claude Code
# from invoking the skill unasked, deciding only plain frontmatter: the file
# is UTF-8 (a byte-order mark dropped, CRLF read as LF); its first line is
# `---` and the next line that is `---` closes the block, trailing
# whitespace allowed on either; no line of the block holds `---`, which
# Claude Code's own pattern would close it at, or breaks other than at LF
# or CRLF; the block parses as a YAML mapping; and
# disable-model-invocation in it is absent or a YAML boolean (true, yes or
# on, in any case). It exits 0 for true, and 1 for false, for the key
# absent, or for a file whose first line is not `---`. Anything else, such
# as a quoted string, a number, YAML that does not parse, a block that
# never closes, or any failure to read the file, exits 2 naming the file
# and why: whether Claude Code reads it as disabled is not settled here,
# so pstackSkill fails the build on it. checks.pstack-skills holds cases of
# each.
{writers}:
writers.writePython3 "disables-model-invocation" {
  libraries = ps: [ps.pyyaml];
} ''
  import sys

  import yaml

  FLAG = "disable-model-invocation"
  # Line breaks YAML knows besides LF and CRLF: CR alone, NEL, and the line
  # and paragraph separators.
  BREAKS = tuple(map(chr, (0x0D, 0x85, 0x2028, 0x2029)))


  class NotPlain(Exception):
      pass


  def disabled(path):
      try:
          with open(path, encoding="utf-8", newline="") as f:
              text = f.read()
      except UnicodeDecodeError as e:
          raise NotPlain(f"it is not UTF-8: {e}")
      lines = text.removeprefix(chr(0xFEFF)).split("\n")
      if lines[0].rstrip() != "---":
          return False
      for end, line in enumerate(lines[1:], 1):
          if line.rstrip() == "---":
              break
      else:
          raise NotPlain("its frontmatter has no closing --- line")
      block = [line.removesuffix(chr(0x0D)) for line in lines[1:end]]
      for line in block:
          if any(b in line for b in BREAKS):
              raise NotPlain("its frontmatter breaks a line other than at LF")
          if "---" in line:
              raise NotPlain(f"its frontmatter line {line!r} holds ---")
      try:
          data = yaml.safe_load("\n".join(block))
      except yaml.YAMLError as e:
          raise NotPlain(f"its frontmatter does not parse as YAML: {e}")
      if not isinstance(data, dict):
          raise NotPlain("its frontmatter is not a YAML mapping")
      if FLAG not in data:
          return False
      if not isinstance(data[FLAG], bool):
          raise NotPlain(f"{FLAG} is {data[FLAG]!r}, not a YAML boolean")
      return data[FLAG]


  # Anything else that goes wrong, such as a missing file or a date pyyaml
  # cannot construct, leaves the answer as unsettled as frontmatter that is
  # not plain.
  try:
      result = disabled(sys.argv[1])
  except NotPlain as e:
      why = str(e)
  except Exception as e:
      why = f"{type(e).__name__}: {e}"
  else:
      sys.exit(0 if result else 1)
  print(f"disables-model-invocation: {sys.argv[1]}: {why}", file=sys.stderr)
  sys.exit(2)
''
