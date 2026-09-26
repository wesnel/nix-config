---
name: trash
description: Always use trash instead of rm. This applies to all file and directory deletion operations.
---

# Use `trash` Instead of `rm`

When deleting files or directories, **always** use the `trash` command instead of `rm`. The `trash` command moves items to the system Trash (macOS) rather than permanently deleting them, making deletions recoverable.

## Rules

- **NEVER** use `rm`, `rm -r`, `rm -rf`, or any variant of `rm` to delete files or directories.
- **ALWAYS** use `trash` instead.
- This applies to all contexts: cleanup, build artifacts, temporary files, generated files, etc.

## Command

```
trash [-s|--stopOnError] [-v|--verbose] <file>...
```

## Translating Common `rm` Invocations

| Instead of (`rm`)                  | Use (`trash`)               | Notes                                                        |
|------------------------------------|-----------------------------|--------------------------------------------------------------|
| `rm file.txt`                      | `trash file.txt`            | Move a single file to Trash                                  |
| `rm file1.txt file2.txt`           | `trash file1.txt file2.txt` | Move multiple files to Trash                                 |
| `rm -r dir/`                       | `trash dir/`                | `trash` handles directories without needing `-r`             |
| `rm -rf dir/`                      | `trash dir/`                | No force flag needed; `trash` does not prompt for confirmation|
| `rm -f file.txt`                   | `trash file.txt`            | No force flag needed                                         |
| `rm *.log`                         | `trash *.log`               | Glob patterns work the same way                              |
| `rm -rf build/ dist/ node_modules/`| `trash build/ dist/ node_modules/` | Multiple directories at once                           |

## Examples

```bash
# Delete a single file
trash output.txt

# Delete multiple files
trash foo.o bar.o baz.o

# Delete a directory and all its contents
trash build/

# Delete with a glob pattern
trash *.pyc

# Delete build artifacts
trash dist/ target/ out/

# Verbose mode (prints each item as it is trashed)
trash -v old-file.txt temp-dir/

# Stop on first error when trashing multiple items
trash -s file1.txt file2.txt file3.txt
```

## Key Differences from `rm`

- `trash` moves items to the system Trash; they can be recovered via Finder or `put-back`.
- No `-r` or `-R` flag is needed for directories — `trash` recurses automatically.
- No `-f` flag is needed — `trash` does not prompt for confirmation.
- No `-i` interactive mode — if you need confirmation before deleting, check manually first.
- If a file does not exist, `trash` will print an error but continue processing remaining arguments (unless `-s`/`--stopOnError` is set).
