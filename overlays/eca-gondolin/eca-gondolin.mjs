// Runs the ECA server inside a Gondolin micro-VM and relays its JSON-RPC over
// stdio.
//
// The channel has to be byte-exact: ECA frames messages as
// `Content-Length: N\r\n\r\n`, which a PTY's line discipline would corrupt by
// echoing input back and translating \n to \r\n. The SDK's exec is raw, so
// this deliberately does not use the `gondolin bash` CLI, which attaches a
// PTY.
//
// Point `eca-custom-command' at this, per project rather than globally:
// setting it at all takes precedence over remote lookup, so a global value
// would hijack TRAMP sessions that should resolve `eca' on their own host.

const lib = process.env.ECA_GONDOLIN_LIB;

if (!lib) {
  process.stderr.write("eca-gondolin: ECA_GONDOLIN_LIB is unset\n");
  process.exit(2);
}

const {VM, createHttpHooks, RealFSProvider, ReadonlyProvider} = await import(lib);

const parseArgs = (argv) => {
  const opts = {
    workspace: process.env.ECA_GONDOLIN_WORKSPACE || process.cwd(),
    guestPath: process.env.ECA_GONDOLIN_GUEST_PATH || null,
    eca: process.env.ECA_GONDOLIN_ECA || null,
    image: process.env.ECA_GONDOLIN_IMAGE || null,
    log: process.env.ECA_GONDOLIN_LOG || null,
    config: process.env.ECA_GONDOLIN_CONFIG || null,
    state: process.env.ECA_GONDOLIN_STATE || null,
    // No default: the allowlist has to be able to express "nothing", which it
    // cannot if some host is always in it. ECA starts without reaching the
    // model catalogue, so there is nothing that must be reachable.
    allowedHosts: (process.env.ECA_GONDOLIN_ALLOW_HOSTS || "")
      .split(",")
      .map((h) => h.trim())
      .filter(Boolean),
    tcpMaps: [],
    env: {},
    command: [],
  };

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];

    if (arg === "--") {
      opts.command = argv.slice(i + 1);
      break;
    } else if (arg === "--workspace") {
      opts.workspace = argv[++i];
    } else if (arg === "--guest-path") {
      opts.guestPath = argv[++i];
    } else if (arg === "--eca") {
      opts.eca = argv[++i];
    } else if (arg === "--image") {
      opts.image = argv[++i];
    } else if (arg === "--config") {
      opts.config = argv[++i];
    } else if (arg === "--state") {
      opts.state = argv[++i];
    } else if (arg === "--log") {
      opts.log = argv[++i];
    } else if (arg === "--allow-host") {
      opts.allowedHosts.push(argv[++i]);
    } else if (arg === "--tcp-map") {
      opts.tcpMaps.push(argv[++i]);
    } else if (arg === "--env") {
      const [key, ...rest] = argv[++i].split("=");
      opts.env[key] = rest.join("=");
    } else {
      process.stderr.write(`eca-gondolin: unknown option ${arg}\n`);
      process.exit(2);
    }
  }

  return opts;
};

const opts = parseArgs(process.argv.slice(2));

const path = await import("node:path");
const fs = await import("node:fs");

// Every tool the agent runs is a child of the server process, so the VM
// boundary covers the whole tool surface rather than just shell commands.
const mounts = {};

const workspace = path.resolve(opts.workspace);

// Mounting the workspace where it already lives means the paths the server
// reports are the paths the editor already has, so no prefix map is needed in
// either direction. Over TRAMP that is the difference between working and
// not: the outbound translation strips the TRAMP prefix before applying any
// map, while the inbound one never puts it back, so an explicit mapping
// cannot satisfy both directions at once.
const guestPath = opts.guestPath ?? workspace;

mounts[guestPath] = new RealFSProvider(workspace);

// The guest image has no copy of the server, so a Linux build is mounted
// read-only rather than baked in.
if (opts.eca) {
  mounts["/opt/eca"] = new ReadonlyProvider(
    new RealFSProvider(path.resolve(path.dirname(opts.eca))),
  );
}

// Skills are read from ~/.config/eca/skills, and logins and chat history are
// written under ~/.cache/eca. Without both mounted the guest starts from
// nothing every session: no skills, and a login that has to be redone.
const GUEST_HOME = "/root";

if (opts.config) {
  const config = path.resolve(opts.config);

  if (fs.existsSync(config)) {
    mounts[`${GUEST_HOME}/.config/eca`] = new ReadonlyProvider(
      new RealFSProvider(config),
    );
  }
}

if (opts.state) {
  const state = path.resolve(opts.state);

  fs.mkdirSync(state, {recursive: true});

  mounts[`${GUEST_HOME}/.cache/eca`] = new RealFSProvider(state);
}

const command = opts.command.length
  ? opts.command
  : [opts.eca ? `/opt/eca/${path.basename(opts.eca)}` : "eca", "server"];

const appendLog = (record) => {
  if (!opts.log) return;

  try {
    fs.appendFileSync(opts.log, JSON.stringify(record) + "\n");
  } catch {
    // Observability is best-effort: a failed write must not take the
    // session down.
  }
};

// Gondolin terminates TLS with a CA it mints and installs in the guest trust
// store, so these see decrypted requests without the server being configured
// to trust anything.
// Returns the hooks alongside the environment the guest needs for secret
// placeholders, so both have to be destructured rather than passed through
// whole.
const {httpHooks, env} = createHttpHooks({
  allowedHosts: opts.allowedHosts,

  onRequest: (req) => {
    appendLog({
      at: new Date().toISOString(),
      dir: "request",
      method: req?.method,
      url: req?.url,
    });
  },

  // The response itself carries no URL; it arrives with the request it
  // answers, and a request logged without a matching response was refused.
  onResponse: (res, req) => {
    appendLog({
      at: new Date().toISOString(),
      dir: "response",
      status: res?.status,
      url: req?.url,
    });
  },
});

// GUEST_HOST[:PORT]=UPSTREAM_HOST:PORT. A guest name has to be synthetic:
// `localhost` resolves inside the VM and never reaches the resolver that
// would map it back to a service on this machine.
const tcpHosts = {};

for (const spec of opts.tcpMaps) {
  const eq = spec.indexOf("=");

  if (eq <= 0 || eq === spec.length - 1) {
    process.stderr.write(
      `eca-gondolin: expected GUEST_HOST[:PORT]=UPSTREAM_HOST:PORT, got ${spec}\n`,
    );
    process.exit(2);
  }

  tcpHosts[spec.slice(0, eq).trim()] = spec.slice(eq + 1).trim();
}

const mapped = Object.keys(tcpHosts).length > 0;

const vm = new VM({
  vfs: {mounts},
  httpHooks,

  // The guest image points the XDG variables at /tmp, and the server resolves
  // its config and cache through those rather than through HOME, so setting
  // HOME alone leaves both mounts unused: skills stay invisible and logins are
  // discarded with the VM.
  env: {
    ...(env ?? {}),
    HOME: GUEST_HOME,
    XDG_CONFIG_HOME: `${GUEST_HOME}/.config`,
    XDG_CACHE_HOME: `${GUEST_HOME}/.cache`,
    ...opts.env,
  },

  // Anything not resolvable is unreachable, which is what makes the
  // allowlist an enforced boundary rather than a cooperative one.
  dns: {
    mode: "synthetic",
    ...(mapped ? {syntheticHostMapping: "per-host"} : {}),
  },

  // A mapping is itself the grant: mapped hosts are reachable without being
  // on the allowlist, which still governs everything else.
  ...(mapped ? {tcp: {hosts: tcpHosts}} : {}),

  // The stock guest is Alpine, whose musl has no glibc loader, so the
  // server's native build cannot start there. An image built with `gcompat'
  // supplies /lib/ld-linux-aarch64.so.1.
  ...(opts.image ? {sandbox: {imagePath: opts.image}} : {}),
});

let closed = false;

const shutdown = async (code) => {
  if (closed) return;
  closed = true;

  try {
    await vm.close();
  } catch {
    // Already gone.
  }

  process.exit(code);
};

for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(signal, () => void shutdown(0));
}

await vm.start();

// A single string runs through the guest's login shell, which is what makes
// the executable resolvable from $PATH. The array form skips the shell and so
// skips $PATH too, and passing `argv' alongside a string would hand the
// entries to the shell as positional parameters rather than as arguments.
const quote = (s) => `'${String(s).replaceAll("'", `'\\''`)}'`;

const proc = vm.exec(command.map(quote).join(" "), {
  cwd: guestPath,
  stdin: process.stdin,
  stdout: "pipe",
  stderr: "pipe",
});

const relay = async (pipe, sink) => {
  if (!pipe) return;

  for await (const chunk of pipe) {
    sink.write(chunk);
  }
};

await Promise.all([
  relay(proc.session.stdoutPipe, process.stdout),
  relay(proc.session.stderrPipe, process.stderr),
]);

const result = await proc.result;

await shutdown(result?.exitCode ?? 0);
