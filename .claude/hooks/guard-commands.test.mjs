import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { forbiddenReason, REASONS } from "./guard-commands.mjs";

const SCRIPT = path.join(path.dirname(fileURLToPath(import.meta.url)), "guard-commands.mjs");

function expectReason(reason, commands) {
  for (const command of commands) {
    assert.equal(forbiddenReason(command), reason, command);
  }
}

function expectAllowed(commands) {
  for (const command of commands) {
    assert.equal(forbiddenReason(command), null, command);
  }
}

test("gh pr merge --admin is blocked; other merges are not", () => {
  expectReason(REASONS.admin, [
    "gh pr merge --admin",
    "gh pr merge 101 --merge --admin",
    "cd repo && gh pr merge 101 --squash --admin",
  ]);
  expectAllowed([
    "gh pr merge 101 --merge --match-head-commit abc123",
    "gh pr view 101",
    "rg -n -- '--admin' AGENTS.md",
  ]);
});

test("pushes that land on main are blocked", () => {
  expectReason(REASONS.pushMain, [
    "git push origin main",
    "git push -u origin main",
    "git push origin HEAD:main",
    "git push origin dev:main",
    "git push origin :main",
    "git push origin +main",
    "git push origin refs/heads/main",
    "git push origin HEAD:refs/heads/main",
    "git push --delete origin main",
    "git push --mirror origin",
    "git push --all origin",
    "git -C ../repo push origin main",
    "git fetch origin && git push origin main",
  ]);
});

test("pushes to dev and other branches are allowed", () => {
  expectAllowed([
    "git push",
    "git push origin",
    "git push origin dev",
    "git push -u origin HEAD:dev",
    "git push origin main:dev",
    "git push origin feature/main-menu",
    "git push origin claude/fix-main-loop",
    "git push --tags origin",
    "git push origin v1.8.0",
  ]);
});

test("commits without a DCO sign-off are blocked", () => {
  expectReason(REASONS.signoff, [
    "git commit -m 'fix: x'",
    "git commit",
    "git commit -am 'fix: x'",
    "git commit -S -m 'fix: x'",
    "git commit -ms",
    "git commit --no-signoff -m x",
    "git commit -s --no-signoff -m x",
    "git -C ../repo commit -m x",
    "git -c core.editor=true commit",
    "GIT_EDITOR=true git commit",
    "git add -A && git commit -m 'feat: y'",
    "git commit -m \"$(cat <<'EOF'\nfix: x\n\nbody\nEOF\n)\"",
  ]);
});

test("signed-off commits and amends are allowed", () => {
  expectAllowed([
    "git commit -s -m 'fix: x'",
    "git commit --signoff -m 'fix: x'",
    "git commit -sm 'fix: x'",
    "git commit -asm 'fix: x'",
    "git commit -m 'fix: x' -s",
    "git commit -sS -m x",
    "git commit --amend --no-edit",
    "git commit --amend -m 'fix: reword'",
    "git commit -s -F - <<'EOF'\nfix: x\nEOF",
    "git commit -s -m \"$(cat <<'EOF'\nfix: x\n\nSigned-off-by: A <a@example.com>\nEOF\n)\"",
    "git log --oneline -5",
    "git commit-tree HEAD^{tree} -m x",
  ]);
});

test("commit messages that mention forbidden commands are not parsed as commands", () => {
  expectAllowed([
    "git commit -s -m 'docs: never run gh pr merge --admin or git push origin main'",
    "git commit -s -m \"docs: explain why git commit without -s is blocked\"",
    "git commit -s -F - <<'EOF'\ndocs: guard\n\ngit push origin main\ngh pr merge 1 --admin\ngit add -f configs/config.yaml\nEOF",
    "echo 'git push origin main'",
    "rg -n 'git add -f keys/' .claude/hooks",
  ]);
});

test("force-adding ignored secrets or local state is blocked", () => {
  expectReason(REASONS.forceAdd, [
    "git add -f configs/config.yaml",
    "git add --force ./configs/config.yaml",
    "git add -f configs/hooks.yaml",
    "git add -f AuthKey_ABC123.p8",
    "git add -f keys/AuthKey.p8",
    "git add -f keys",
    "git add -f keys/",
    "git add -f data/amdl.db",
    "git add -f data",
    "git add -fA",
    "git add -f -A",
    "git add -f .",
  ]);
});

test("ordinary adds and adds of example files are allowed", () => {
  expectAllowed([
    "git add configs/config.example.yaml",
    "git add -f configs/config.example.yaml",
    "git add configs/config.yaml",
    "git add -A",
    "git add internal/api/server.go docs/api.md",
    "git add -f tools/repocheck/docs_test.go",
  ]);
});

function runHook(stdin) {
  return spawnSync(process.execPath, [SCRIPT], { input: stdin, encoding: "utf8" });
}

test("as a PreToolUse hook: exit 2 with the reason on stderr, silent exit 0 otherwise", () => {
  const hit = runHook(JSON.stringify({ tool_name: "Bash", tool_input: { command: "git push origin main" } }));
  assert.equal(hit.status, 2);
  assert.match(hit.stderr, /^Blocked: .*\(AGENTS\.md: Commits and releases\)\. Do .* instead\.\n$/);
  const miss = runHook(JSON.stringify({ tool_name: "Bash", tool_input: { command: "go test ./..." } }));
  assert.equal(miss.status, 0);
  assert.equal(miss.stdout + miss.stderr, "");
});

test("input that is not the hook JSON passes through", () => {
  for (const stdin of ["", "not json", "{}", JSON.stringify({ tool_input: { command: 42 } })]) {
    const result = runHook(stdin);
    assert.equal(result.status, 0, JSON.stringify(stdin));
    assert.equal(result.stderr, "", JSON.stringify(stdin));
  }
});

test("every reason follows the Blocked/AGENTS.md/Do format", () => {
  for (const reason of Object.values(REASONS)) {
    assert.match(reason, /\(AGENTS\.md: [^)]+\)\. Do .+ instead\.$/);
  }
});
