// Decides, after each turn, whether the assistant has more to do.
//
// ECA runs this as a `postRequest' hook and reads a JSON object from stdout:
// `followUp' starts another turn with that text, and `continue: false' ends
// the run. Both are honoured only on exit 0.
//
// The follow-up text is built from the goal file and never from the model, so
// a run cannot grow past the goals it started with. A goal is finished when
// its own `check' command succeeds -- an answer the model reports but does not
// decide -- and abandoned once it has had its attempts, so a goal that cannot
// be reached costs a bounded number of turns instead of the whole night.

import fs from "node:fs";
import path from "node:path";
import {execFileSync} from "node:child_process";

const fail = (message) => {
  process.stderr.write(`eca-overnight: ${message}\n`);

  // Anything other than 0 or 2 is reported and otherwise ignored, which is
  // the right way for a broken hook to behave: the turn still ends, and it
  // ends without inventing a follow-up.
  process.exit(1);
};

const workspace = process.env.ECA_WORKSPACE_ROOT || process.cwd();
const dir = path.join(workspace, ".eca");
const goalsPath = path.join(dir, "overnight.json");

if (!fs.existsSync(goalsPath)) {
  // No goal file means this is an ordinary session that happens to have the
  // hook configured, so it must end where the user left it.
  process.stdout.write("{}\n");
  process.exit(0);
}

let spec;

try {
  spec = JSON.parse(fs.readFileSync(goalsPath, "utf8"));
} catch (error) {
  fail(`${goalsPath}: ${error.message}`);
}

const goals = Array.isArray(spec.goals) ? spec.goals : [];

if (goals.length === 0) fail(`${goalsPath}: no goals`);

for (const [index, goal] of goals.entries()) {
  if (!goal || typeof goal.id !== "string" || typeof goal.goal !== "string") {
    fail(`goal ${index}: needs an "id" and a "goal"`);
  }

  if (typeof goal.check !== "string" || goal.check.trim() === "") {
    // Without one there is nothing but the model's own account to say the
    // goal is met, which is the judgement this is here to avoid.
    fail(`goal ${goal.id}: needs a "check" command`);
  }
}

const attemptsAllowed = spec.attemptsPerGoal ?? 3;
const statePath = path.join(dir, "overnight-state.json");
const journalPath = path.join(dir, "overnight-journal.jsonl");

let state = {attempts: {}, startedAt: new Date().toISOString()};

if (fs.existsSync(statePath)) {
  try {
    state = JSON.parse(fs.readFileSync(statePath, "utf8"));
  } catch {
    // A corrupt state file would otherwise strand the run; starting the
    // counts again costs at most one extra pass per goal.
  }
}

state.attempts ??= {};

const journal = (record) => {
  try {
    fs.appendFileSync(
      journalPath,
      JSON.stringify({at: new Date().toISOString(), ...record}) + "\n",
    );
  } catch {
    // The journal is for the morning, not for the run: losing a line must
    // not end the night.
  }
};

const save = () => {
  try {
    fs.writeFileSync(statePath, JSON.stringify(state, null, 2) + "\n");
  } catch (error) {
    fail(`${statePath}: ${error.message}`);
  }
};

const stop = (reason) => {
  journal({event: "stop", reason});
  save();

  process.stdout.write(JSON.stringify({continue: false, stopReason: reason}) + "\n");
  process.exit(0);
};

// A run that outlives the night would still be holding the working tree in
// the morning, so the deadline is a wall-clock time rather than a count.
if (typeof spec.until === "string") {
  const [hours, minutes] = spec.until.split(":").map(Number);

  if (Number.isInteger(hours) && Number.isInteger(minutes)) {
    const deadline = new Date();

    deadline.setHours(hours, minutes, 0, 0);

    // A time already past today is tomorrow's: these runs start at night and
    // are meant to end the following morning.
    if (deadline <= new Date(state.startedAt)) {
      deadline.setDate(deadline.getDate() + 1);
    }

    if (new Date() >= deadline) stop(`reached the ${spec.until} deadline`);
  }
}

const check = (goal) => {
  try {
    const stdout = execFileSync("/bin/sh", ["-c", goal.check], {
      cwd: workspace,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      timeout: (spec.checkTimeoutSeconds ?? 600) * 1000,
    });

    return {met: true, output: stdout};
  } catch (error) {
    const output = [error.stdout, error.stderr].filter(Boolean).join("\n");

    return {met: false, output: output || String(error.message)};
  }
};

const summarise = (output) => {
  const lines = output.trimEnd().split("\n");
  const tail = lines.slice(-(spec.checkOutputLines ?? 40));

  return (lines.length > tail.length ? "...\n" : "") + tail.join("\n");
};

for (const goal of goals) {
  const result = check(goal);

  if (result.met) {
    if (state.attempts[goal.id] !== "met") {
      journal({event: "met", goal: goal.id});
      state.attempts[goal.id] = "met";
    }

    continue;
  }

  const attempts = typeof state.attempts[goal.id] === "number" ? state.attempts[goal.id] : 0;

  if (attempts >= attemptsAllowed) continue;

  state.attempts[goal.id] = attempts + 1;

  journal({
    event: "attempt",
    goal: goal.id,
    attempt: attempts + 1,
    of: attemptsAllowed,
  });

  save();

  // Handing over the command and its output makes the goal checkable by the
  // assistant on its own, rather than something it has to be told it failed.
  const followUp = [
    `Work on this goal:`,
    ``,
    goal.goal,
    ``,
    `It is met when this command exits 0:`,
    ``,
    `    ${goal.check}`,
    ``,
    `That command currently fails. Its most recent output was:`,
    ``,
    summarise(result.output),
    ``,
    `This is attempt ${attempts + 1} of ${attemptsAllowed} for this goal.`,
    `Change only what this goal needs, and do not take on work beyond it.`,
  ].join("\n");

  process.stdout.write(JSON.stringify({followUp}) + "\n");
  process.exit(0);
}

const met = goals.filter((goal) => state.attempts[goal.id] === "met");
const abandoned = goals.filter((goal) => state.attempts[goal.id] !== "met");

stop(
  abandoned.length === 0
    ? `all ${goals.length} goals met`
    : `${met.length} of ${goals.length} goals met; ` +
      `no attempts left for ${abandoned.map((goal) => goal.id).join(", ")}`,
);
