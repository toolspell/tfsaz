// Use the adapter and dependency versions from the selected T3 checkout.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { pathToFileURL } from "node:url";

const [root, cwd, web, boardsCwd, organization] = process.argv.slice(2);
const pkg = path.join(root, "packages/source-control-azure-devops");
const load = (name) => import(import.meta.resolve(name, pathToFileURL(path.join(pkg, "package.json")).href));
const adapter = (file) => import(pathToFileURL(path.join(pkg, "src/server", file)).href);
const [Effect, Layer, NodeServices, host, az, pulls, provider, boards] = await Promise.all([
  load("effect/Effect"), load("effect/Layer"), load("@effect/platform-node/NodeServices"),
  load("@t3tools/source-control-testing/TestSourceControlHost"),
  adapter("AzureDevOpsCli.ts"), adapter("AzureDevOpsPullRequestCli.ts"),
  adapter("AzureDevOpsPullRequestProvider.ts"), adapter("AzureDevOpsIssueTracker.ts"),
]);

const { makeSourceControlDefinitionRegistry } = await load("@t3tools/source-control-core/shared/definition");
const { definition } = await import(pathToFileURL(path.join(pkg, "src/shared/definition.ts")).href);
const definitions = makeSourceControlDefinitionRegistry([definition]);
const remote = execFileSync("git", ["remote", "get-url", "origin"], { cwd, encoding: "utf8" }).trim();
assert.equal(definitions.recognizeRemote(remote).kind, "azure-devops");
assert.equal(definitions.repositoryKeyFromRemote(remote), definitions.repositoryKeyFromRemote(web));
const repository = "Tool Kit";
const pr = { cwd, host: new URL(web).host, repository, number: 42 };
const program = Effect.gen(function* () {
  const cli = yield* az.AzureDevOpsCli;
  const branch = yield* cli.getDefaultBranch({ cwd, repository, remoteUrl: remote });
  assert.equal(branch, "main");
  const service = yield* provider.make;
  const viewer = yield* service.getViewer(pr);
  assert.equal(viewer, "author@example.invalid");
  const list = yield* service.listChangeRequests({
    ...pr, viewer, involvement: "authored", state: "open", limit: 10,
  });
  assert.equal(list.items[0]?.url, web + "/pullrequest/42");
  for (const expected of ["pending", "passing"]) {
    const summary = yield* service.getChangeRequestSummary(pr);
    assert.equal(summary.state, "open");
    assert.equal(summary.url, web + "/pullrequest/42");
    assert.equal(summary.checksState, expected);
  }
  const pullCli = yield* pulls.AzureDevOpsPullRequestCli;
  const location = { project: "Shared Project", repository };
  const labels = yield* pullCli.listLabels({ cwd, number: 42, location });
  assert.deepEqual(labels.map(({ name }) => name), ["fixture-label"]);
  const activity = yield* service.getChangeRequestActivity(pr);
  assert.equal(activity.commentsTruncated, false);
  assert.deepEqual(activity.comments.map(({ body }) => body), ["Please update the example."]);
  const diff = yield* service.getDiff(pr);
  assert.equal(diff.truncated, false);
  assert.equal(diff.nextCursor, null);
  assert.match(diff.patch, /-original line\n\+updated line\n/);
  assert.match(diff.patch, /\+extra line\n/);
  const item = "docs/notes #1&+%2F.md";
  const contents = yield* service.getDiffFileContents({
    ...pr, changeType: "change", oldPath: item, newPath: item,
  });
  assert.equal(contents.oldContents, "original line\n");
  assert.equal(contents.newContents, "updated line\n");
  // A leading @ makes the stock adapter send the text through stdin.
  yield* pullCli.updatePullRequest({ ...pr, body: "@fixture body — Grüße\n第二行" });
});
const support = Layer.mergeAll(NodeServices.layer, host.layerWithRealGit());
const deps = pulls.layer.pipe(Layer.provideMerge(az.layer), Layer.provide(support));
await Effect.runPromise(program.pipe(Effect.provide(deps)));

const boardsProgram = Effect.gen(function* () {
  const tracker = yield* boards.make;
  // Boards can select a configured organization and project outside a Git checkout.
  const scope = { cwd: boardsCwd, host: new URL(organization).host, scope: "Shared Project" };
  const list = {
    ...scope, query: "reader's focus", labels: ["interface"], state: "open",
    involvement: "assigned", viewer: "@Me", sort: "updated", limit: 2,
  };
  const first = yield* tracker.listIssues(list);
  const project = organization + "/Shared%20Project";
  assert.deepEqual(first.items.map(({ key, url }) => [key, url]), [
    ["9", project + "/_workitems/edit/9"],
    ["7", project + "/_workitems/edit/7"],
  ]);
  assert.equal(first.nextCursor, "2026-07-03T00:00:00Z|7");
  const second = yield* tracker.listIssues({ ...list, cursor: first.nextCursor });
  assert.deepEqual(second.items.map(({ key }) => key), ["3"]);
  assert.equal(second.nextCursor, null);
  assert.deepEqual(yield* tracker.listIssues({ ...list, query: "no match" }), {
    items: [], nextCursor: null,
  });

  const target = { ...scope, key: "7" };
  const issue = yield* tracker.getIssue(target);
  assert.equal(issue.url, project + "/_workitems/edit/7");
  assert.equal(issue.title, "Reader's focus");
  assert.equal(issue.body, "<p>Moving the cursor drops focus.</p>");
  assert.deepEqual(issue.state, { category: "closed", name: "Shipped" });
  assert.deepEqual(issue.states, [
    { category: "open", name: "Active" },
    { category: "closed", name: "Shipped" },
  ]);
  assert.deepEqual(issue.labels.map(({ name }) => name), ["bug", "interface"]);
  assert.deepEqual(issue.linkedPullRequests.map(({ number, url }) => [number, url]), [
    [42, project + "/_git/Tool%20Kit/pullrequest/42"],
  ]);
  assert.equal(issue.commentCount, 3);
  assert.deepEqual(issue.comments.map(({ body }) => body), ["Repro attached.", "Still happening."]);
  assert.deepEqual(issue.comments[0].reactions, [
    { content: "thumbs-up", count: 2, actors: [], viewerHasReacted: true },
  ]);
  assert.equal(issue.commentsCursor, "next +/&=%2F");
  const older = yield* tracker.getComments({ ...target, cursor: issue.commentsCursor });
  assert.deepEqual(older.comments.map(({ body }) => body), ["First report."]);
  assert.equal(older.nextCursor, null);
  assert.deepEqual(issue.events.map(({ kind, detail, createdAt }) => [kind, detail, createdAt]), [
    ["renamed", "Reader's focus", "2026-07-02T00:00:00Z"],
    ["closed", "Shipped", "2026-07-03T00:00:00Z"],
    ["labeled", "bug", "2026-07-03T00:00:00Z"],
  ]);

  const candidates = yield* tracker.listCandidates(scope);
  assert.deepEqual(candidates.labels.map(({ name }) => name), ["bug", "interface"]);
  assert.deepEqual(candidates.assignees.map(({ login, name }) => [login, name]), [
    ["author@example.invalid", "Fixture Author"],
    ["reviewer@example.invalid", "Fixture Reviewer"],
  ]);
  assert.equal(candidates.truncated, true);
  // Stock T3 stages the comment as JSON in a temporary request file.
  yield* tracker.comment({ ...target, body: "@fixture comment — café\n再現手順" });
});
await Effect.runPromise(boardsProgram.pipe(Effect.provide(az.layer.pipe(Layer.provideMerge(support)))));
