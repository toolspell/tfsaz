// Use the adapter and dependency versions from the selected T3 checkout.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { pathToFileURL } from "node:url";

const [root, cwd, web] = process.argv.slice(2);
const pkg = path.join(root, "packages/source-control-azure-devops");
const load = (name) => import(import.meta.resolve(name, pathToFileURL(path.join(pkg, "package.json")).href));
const adapter = (file) => import(pathToFileURL(path.join(pkg, "src/server", file)).href);
const [Effect, Layer, NodeServices, host, az, pulls, provider] = await Promise.all([
  load("effect/Effect"), load("effect/Layer"), load("@effect/platform-node/NodeServices"),
  load("@t3tools/source-control-testing/TestSourceControlHost"),
  adapter("AzureDevOpsCli.ts"), adapter("AzureDevOpsPullRequestCli.ts"),
  adapter("AzureDevOpsPullRequestProvider.ts"),
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
});
const support = Layer.mergeAll(NodeServices.layer, host.layerWithRealGit());
const deps = pulls.layer.pipe(Layer.provideMerge(az.layer), Layer.provide(support));
await Effect.runPromise(program.pipe(Effect.provide(deps)));
