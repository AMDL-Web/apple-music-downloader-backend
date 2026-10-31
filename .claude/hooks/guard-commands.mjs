#!/usr/bin/env node
// PreToolUse guard for Claude Code's Bash tool. Reads the hook JSON on stdin
// and exits 2 with a reason on stderr when the command breaks a rule that
// AGENTS.md states in text. Every rule here also stays written in AGENTS.md,
// because some agent environments do not run repository hooks.
//
// Blocked:
//   - gh pr merge with --admin            (AGENTS.md: Merging with `--admin`)
//   - git push whose destination is main  (AGENTS.md: Commits and releases)
//   - git commit without -s / --signoff   (AGENTS.md: Commits and releases)
//   - git add -f of configs/config.yaml, configs/hooks.yaml, *.p8, keys/, data/
//                                         (AGENTS.md: Data safety)
//
// Input that is not the expected JSON passes through (exit 0): a guard that
// failed closed on a harness format change would block every Bash call.

import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

// Heredoc bodies are data (commit messages, file contents), so a message that
// merely mentions a forbidden command must not trip the guard.
function stripHeredocs(command) {
  const kept = [];
  let delimiter = null;
  for (const line of command.split("\n")) {
    if (delimiter !== null) {
      if (line.trim() === delimiter) delimiter = null;
      continue;
    }
    kept.push(line);
    const match = line.match(/<<-?\s*(['"]?)([\w.-]+)\1/);
    if (match) delimiter = match[2];
  }
  return kept.join("\n");
}

// segments splits a shell command into simple commands (on ; & | newlines,
// parentheses, backticks and $( ), each a list of tokens. Quoted text stays
// one token, so a quoted commit message is never parsed as a command.
export function segments(command) {
  const text = stripHeredocs(command);
  const result = [];
  let tokens = [];
  let token = "";
  let quoted = false;
  const endToken = () => {
    if (token !== "" || quoted) tokens.push(token);
    token = "";
    quoted = false;
  };
  const endSegment = () => {
    endToken();
    if (tokens.length > 0) result.push(tokens);
    tokens = [];
  };
  for (let i = 0; i < text.length; i += 1) {
    const char = text[i];
    if (char === "'") {
      const end = text.indexOf("'", i + 1);
      token += text.slice(i + 1, end === -1 ? text.length : end);
      quoted = true;
      i = end === -1 ? text.length : end;
    } else if (char === '"') {
      let j = i + 1;
      while (j < text.length && text[j] !== '"') {
        if (text[j] === "\\" && j + 1 < text.length) j += 1;
        token += text[j];
        j += 1;
      }
      quoted = true;
      i = j;
    } else if (char === "\\" && i + 1 < text.length) {
      token += text[i + 1];
      i += 1;
    } else if (char === "$" && text[i + 1] === "(") {
      endSegment();
      i += 1;
    } else if (";&|\n()`".includes(char)) {
      endSegment();
    } else if (/\s/.test(char)) {
      endToken();
    } else {
      token += char;
    }
  }
  endSegment();
  return result;
}

const program = (token) => path.posix.basename(token);
const ASSIGNMENT = /^[A-Za-z_]\w*=/;

// Global git options that take a separate value before the subcommand.
const GIT_OPTIONS_WITH_VALUE = new Set(["-c", "-C", "--config-env", "--git-dir", "--work-tree", "--namespace"]);

// gitCommand returns the subcommand and its arguments when the segment runs
// git (after any VAR=value prefixes or an env wrapper), else null.
function gitCommand(tokens) {
  let i = 0;
  while (i < tokens.length && (ASSIGNMENT.test(tokens[i]) || program(tokens[i]) === "env" || program(tokens[i]) === "command")) i += 1;
  if (i >= tokens.length || program(tokens[i]) !== "git") return null;
  const rest = tokens.slice(i + 1);
  for (let j = 0; j < rest.length; j += 1) {
    if (GIT_OPTIONS_WITH_VALUE.has(rest[j])) {
      j += 1;
    } else if (!rest[j].startsWith("-")) {
      return { sub: rest[j], args: rest.slice(j + 1) };
    }
  }
  return null;
}

// commit options whose value is the next token; their value is never a flag.
const COMMIT_OPTIONS_WITH_VALUE = new Set([
  "-m", "--message", "-F", "--file", "-C", "--reuse-message", "-c", "--reedit-message",
  "-t", "--template", "--author", "--date", "--fixup", "--squash", "--trailer", "--cleanup",
  "--pathspec-from-file",
]);
// Short options that consume the rest of a flag cluster as their value.
const SHORT_VALUE_FLAGS = new Set(["m", "F", "C", "c", "t", "S", "u"]);

function commitWithoutSignoff(args) {
  let signoff = false;
  let amend = false;
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === "--") break;
    if (COMMIT_OPTIONS_WITH_VALUE.has(arg)) {
      i += 1;
      continue;
    }
    if (arg === "--signoff" || arg === "--signoff=true") signoff = true;
    else if (arg === "--no-signoff") signoff = false;
    else if (arg === "--amend") amend = true;
    else if (/^-[A-Za-z]/.test(arg) && !arg.startsWith("--")) {
      for (const flag of arg.slice(1)) {
        if (flag === "s") signoff = true;
        if (SHORT_VALUE_FLAGS.has(flag)) break;
      }
    }
  }
  return !signoff && !amend;
}

const PUSH_OPTIONS_WITH_VALUE = new Set(["--repo", "-o", "--push-option", "--receive-pack", "--exec"]);
const MAIN_REF = /^(?:refs\/heads\/)?main$/;

function pushTargetsMain(args) {
  const positionals = [];
  let deleting = false;
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (PUSH_OPTIONS_WITH_VALUE.has(arg)) {
      i += 1;
    } else if (arg === "--mirror" || arg === "--all" || arg === "--branches") {
      return true;
    } else if (arg === "--delete" || arg === "-d") {
      deleting = true;
    } else if (arg === "--") {
      positionals.push(...args.slice(i + 1));
      break;
    } else if (!arg.startsWith("-")) {
      positionals.push(arg);
    }
  }
  return positionals.slice(1).some((refspec) => {
    const spec = refspec.replace(/^\+/, "");
    const destination = deleting || !spec.includes(":") ? spec : spec.slice(spec.indexOf(":") + 1);
    return MAIN_REF.test(destination);
  });
}

const PROTECTED_PATH = /^(?:configs\/(?:config|hooks)\.yaml|.*\.p8|(?:.*\/)?keys(?:\/.*)?|data(?:\/.*)?|\.|\*)$/;

function forceAddsProtected(args) {
  const force = args.some((arg) => arg === "--force" || /^-[A-Za-z]*f[A-Za-z]*$/.test(arg));
  if (!force) return false;
  if (args.some((arg) => arg === "-A" || arg === "--all" || /^-[A-Za-z]*A[A-Za-z]*$/.test(arg))) return true;
  return args
    .filter((arg) => !arg.startsWith("-"))
    .map((arg) => arg.replace(/^\.\//, "").replace(/\/+$/, ""))
    .some((arg) => PROTECTED_PATH.test(arg));
}

function ghAdminMerge(tokens) {
  const at = tokens.findIndex((token) => program(token) === "gh");
  if (at === -1) return false;
  const rest = tokens.slice(at + 1);
  return rest[0] === "pr" && rest[1] === "merge" && rest.some((token) => token === "--admin" || token.startsWith("--admin="));
}

export const REASONS = {
  admin:
    "gh pr merge --admin bypasses branch protection, which needs the maintainer's approval for that specific bypass (AGENTS.md: Merging with `--admin`). Do report what blocks the merge (failing checks, missing reviews, open threads) and wait instead.",
  pushMain:
    "main only changes through a reviewed pull request from dev (AGENTS.md: Commits and releases). Do push to dev, or open a dev -> main pull request instead.",
  signoff:
    "every commit needs a DCO Signed-off-by trailer (AGENTS.md: Commits and releases). Do rerun the commit with -s (or --signoff) instead.",
  forceAdd:
    "configs/config.yaml, configs/hooks.yaml, *.p8 keys, keys/ and data/ are git-ignored because they hold secrets or local state (AGENTS.md: Data safety). Do add the example or source file you meant (configs/config.example.yaml, configs/hooks.example.yaml) without -f instead.",
};

export function forbiddenReason(command) {
  for (const tokens of segments(command)) {
    if (ghAdminMerge(tokens)) return REASONS.admin;
    const git = gitCommand(tokens);
    if (git === null) continue;
    if (git.sub === "push" && pushTargetsMain(git.args)) return REASONS.pushMain;
    if (git.sub === "commit" && commitWithoutSignoff(git.args)) return REASONS.signoff;
    if (git.sub === "add" && forceAddsProtected(git.args)) return REASONS.forceAdd;
  }
  return null;
}

function main() {
  let input;
  try {
    input = JSON.parse(fs.readFileSync(0, "utf8"));
  } catch {
    return;
  }
  const command = input?.tool_input?.command;
  if (typeof command !== "string") return;
  const reason = forbiddenReason(command);
  if (reason === null) return;
  process.stderr.write(`Blocked: ${reason}\n`);
  process.exit(2);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) main();
