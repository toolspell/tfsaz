// Use the adapter and dependency versions from the selected T3 checkout.
import assert from "node:assert/strict";
import path from "node:path";
import { pathToFileURL } from "node:url";

const [root, cwd] = process.argv.slice(2);
const pkg = path.join(root, "packages/source-control-azure-devops");
const load = (name) => import(import.meta.resolve(name, pathToFileURL(path.join(pkg, "package.json")).href));
const adapter = (file) => import(pathToFileURL(path.join(pkg, "src/server", file)).href);
const [Effect, Layer, NodeServices, host, az, pulls, provider] = await Promise.all([
  load("effect/Effect"), load("effect/Layer"), load("@effect/platform-node/NodeServices"),
  load("@t3tools/source-control-testing/TestSourceControlHost"),
  adapter("AzureDevOpsCli.ts"), adapter("AzureDevOpsPullRequestCli.ts"),
  adapter("AzureDevOpsPullRequestProvider.ts"),
]);

const org = "https://fixture.visualstudio.com";
const repository = "Tool Kit";
const remote = org + "/DefaultCollection/Shared%20Project/_git/Tool%20Kit";
const pr = { cwd, host: "fixture.visualstudio.com", repository, number: 42 };
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
  assert.equal(list.items[0]?.url, remote + "/pullrequest/42");
  for (const expected of ["pending", "passing"]) {
    const summary = yield* service.getChangeRequestSummary(pr);
    assert.equal(summary.state, "open");
    assert.equal(summary.url, remote + "/pullrequest/42");
    assert.equal(summary.checksState, expected);
  }
});
const support = Layer.mergeAll(NodeServices.layer, host.layerWithRealGit());
const deps = pulls.layer.pipe(Layer.provideMerge(az.layer), Layer.provide(support));
await Effect.runPromise(program.pipe(Effect.provide(deps)));
