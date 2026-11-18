# External probes

Choose the observation that separates the hypotheses, then the tool that
can produce it. Keep the reproducer's inputs and build flags in the
evidence. A debug build can expose locals that optimization removed;
confirm the diagnosis on the original build too. Probes perturb timing,
especially syscall tracing and breakpoints.

| Observation | First tool |
| --- | --- |
| Failed file access, subprocess or socket operation | `strace -f` on Linux |
| CPU hot stack | `perf record`, then `perf report --stdio` |
| Time waiting for I/O or scheduling | `bpftrace` tracepoints, filtered to the workload |
| Existing native function calls or latency | `bpftrace` uprobes after checking symbols and ABI |
| Go branches, locals, goroutines | Delve (`dlv`) |
| Native branches, locals, threads, corrupting writes | GDB or LLDB breakpoints and watchpoints |
| Execution leading up to a captured failure | `rr` replay, only after a capability smoke test |

Use an installed local debugger when it can answer the question without
host root. When Linux tracing or elevated privileges are needed, use the
managed guest below. A Linux reproduction is required for a Linux probe
to explain a host failure; keep macOS-only failures on macOS.

## The isolated guest

Use the `debug` MCP endpoint, whose tools retain the `lima_dev_*` names.
The endpoint selects the dedicated guest; never substitute the `lima` or
`parallels` endpoint. Guest root is preapproved here, including installing
tools and adjusting guest tracing settings. Host permissions still apply.

1. Call `lima_dev_status` on `debug`. If absent or stopped, call
   `lima_dev_start`, then wait for its job with `lima_dev_job_result`.
   Continue only after status reports attested. A mismatch or a missing
   endpoint is an environment blocker, not permission to launch an
   unguarded VM. Report the reason and the profile-specific recovery.
2. In the task worktree run `lima-debug-export NAME`, with a unique name
   for this diagnosis, then call `lima_dev_import` on `debug` with that
   name. Wait for import success. Import replaces guest project state;
   preserve any useful guest results first. Keep source fixes in the host
   worktree and reimport them for verification. Do not import credentials.
3. Use `lima_dev_exec_start` on `debug` for a bounded script. It starts in
   the imported project directory as the guest user; `sudo -n` is available
   inside it. Set `timeout_seconds` for the whole probe, and keep each
   profiler's capture shorter than that budget. Poll job results with
   `wait_seconds` no greater than 60. Report exit status, truncation and
   probe setup failures separately from the program's failure.
4. Keep each debugger session inside its job. Cancel with
   `lima_dev_job_cancel` on `debug` and check the resulting job status;
   report cleanup failures. Guest root can escape guest process controls,
   so cleanup is convenience, while the VM's host isolation is the boundary.
   Coordinate use of the shared guest: import refuses while a job runs.

The guest has no host mounts or forwarded credentials and uses restricted
HTTPS egress. Use fixtures instead of production credentials or live
customer payloads. Read only the needed lines of captured output; raw
traces, debugger locals and core dumps can contain secrets. Leave bulky
captures inside the guest, outside the imported checkout, and report only
redacted evidence. Guest state persists across stop/start; it is not a
fresh environment until the operator recreates it with `lima-debug`.

## Capability check

Run this inside the guest before relying on privileged tracing:

```sh
uname -srmo
sudo -n id -u
strace --version
dlv version
gdb --version
perf --version
sudo -n bpftrace --info
test -r /sys/kernel/btf/vmlinux
sudo -n perf stat -e task-clock -- true
sudo -n bpftrace -l 'tracepoint:syscalls:sys_enter_openat'
```

Missing BTF, tracepoints or perf events is a failed capability check, not
evidence that the program made no calls. Root cannot provide a PMU the
hypervisor does not expose. Use software CPU sampling when hardware
counters are unavailable. Install `rr` only when needed, run `rr record
/bin/true` and replay it before attempting the failing workload; retain
ordinary debugger probes when recording is unsupported.

## Bounded recipes

Replace the program, package, test and symbol names with the reproducer's.
These scripts run in a single guest job; their `/tmp` files are captures.

For failed OS interactions, follow children and include syscall durations:

```sh
capture_dir=$(mktemp -d)
strace -f -tt -T -s 128 -e trace=file,process,network -o "$capture_dir/probe.strace" ./program
```

Inspect the operations near the failure, not every expected `ENOENT` from
library or configuration lookup. Tracing only the parent misses child work.

For CPU stacks, start with a software event that works without a PMU:

```sh
capture_dir=$(mktemp -d)
sudo -n perf record -e cpu-clock -g -o "$capture_dir/probe.perf" -- ./program
sudo -n perf report --stdio -i "$capture_dir/probe.perf"
```

Verify stack unwinding before interpreting missing frames. For an optimized
binary without frame pointers, try `--call-graph dwarf` with its debug
symbols. CPU sampling does not explain off-CPU waiting.

For syscall activity without changing the program:

```sh
sudo -n bpftrace -c './program' -e 'tracepoint:raw_syscalls:sys_enter /pid == cpid/ { @[comm] = count(); }'
```

This filters to the launched process, not descendants. For latency or
waiting, list the available probes and their fields first, then pair
entry/exit events by thread ID and aggregate a histogram. Use a PID or
cgroup filter and a bounded duration; avoid unfiltered system-wide output.
Uprobes require a real symbol/address and the program's calling convention;
Go goroutines and stack growth need Go-aware debugging rather than guessed
native argument layouts.

For a Go test, inspect a breakpoint with Delve's terminal interface driven
through standard input:

```sh
capture_dir=$(mktemp -d)
go test -c -gcflags='all=-N -l' -o "$capture_dir/probe.test" ./pkg
printf 'break example.org/project/pkg.Function\ncontinue\nargs\nlocals\nbt\ngoroutines\nexit\n' | dlv exec "$capture_dir/probe.test" --allow-non-terminal-interactive -- -test.run '^TestName$'
```

Choose a breakpoint reached by the failing invocation. A conditional
breakpoint or watchpoint often answers the question in one stop. For
repeated machine-driven inspection, Delve supports headless JSON-RPC and
DAP; bind it to guest loopback and keep server and client in the same job.

For a native failure, use batch GDB:

```sh
gdb -q -batch -ex 'set pagination off' -ex run -ex 'thread apply all bt' -ex 'info locals' --args ./program
```

For wrong state without a crash, set a breakpoint or watchpoint before
`run`. LLDB has the equivalent batch `-o` command interface. Use native
`lldb-mcp` only after verifying the installed package supplies it and a
session can actually stop at a breakpoint; an upstream feature description
does not establish availability in this environment.

The probe is complete when its observation supports or rejects the named
hypothesis. Record the command, relevant output, build identity and any
capability limits. Repeat the reproducer after the fix, and repeat the
probe when it is needed to establish the cause is gone.
