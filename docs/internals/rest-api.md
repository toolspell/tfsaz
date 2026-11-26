# REST API

TFSaz calls the Azure DevOps Server REST API directly. Two rules hold for every request it builds: resolve the route through the server's resource catalog, and target API version 7.1.

## Resolve routes through the catalog

Azure DevOps Server advertises its REST resources. `OPTIONS <collection>/_apis?allHostTypes=true` returns one resource location per route, with its area, resource name, route template and version range (`minVersion`, `maxVersion`, `releasedVersion`, `resourceVersion`). The extension's SDK resolves every call it makes this way, by location ID, and its `devops invoke` resolves area and resource names against the same list. T3 relies on that: its PR and Boards adapters call `az devops invoke --area ... --resource ...` and never build a URL.

A hard-coded `_apis/...` path skips that contract. It breaks silently when a server moves a route or lacks a resource, and the user gets an HTTP 404 instead of "absent from the collection catalog". So:

- A new REST call resolves its route through the catalog, or reuses a path that is already catalog-resolved.
- A hard-coded route needs a written reason in a comment at the call site. "It is shorter" is not one.
- The existing hard-coded routes are listed in [#17](https://github.com/toolspell/tfsaz/issues/17), with their reasons where one exists.

How TFSaz does it, in `internal/az/catalog.go` and `apiCall.invoke` in `api_call.go`:

- `fetchCatalog` sends the OPTIONS request to the profile's real collection URL on every catalog-resolved command. Nothing caches it, so a server upgrade takes effect at once; the price is one extra request per call (a live 2022.2 collection advertised 1,198 locations).
- TFSaz skips the extension's `_apis/resourceAreas` lookup, which picks the host per area. On Azure DevOps Server every area lives on the collection, and the extension's SDK notes that the server returns an empty list there.
- `resolveResource` matches area and resource case-insensitively, skips locations whose `minVersion` is above the requested version, and binds the route template. Of the matches it keeps the highest `maxVersion`, then the one that omits the fewest optional slots; a remaining tie is an error, not a guess.
- `bindResourceRoute` rejects values that could become path control segments (dot segments, `_apis`, `;`, slashes, also percent-encoded, trailing dots), so a catalog-resolved path stays inside the collection.
- Version negotiation follows the extension's SDK (`_negotiate_request_version` in `devops_sdk/client.py`): a version above `maxVersion` drops to `maxVersion`, with `-preview` when `releasedVersion` is lower, and a preview revision above `resourceVersion` drops to it. The negotiated version goes into the `Accept` header (`application/json;api-version=...`), as the SDK sends it.

Where TFSaz builds routes itself today:

- T3's exact `devops invoke` shapes for Git and work items (`parseStockInvoke` in `invoke.go`, `parseWitInvoke` in `workitems.go`). The reason is written in `cli.go`: TFSaz rewrites their responses (file contents, blob archives, workflow states, work-item links, comment cursors). Any other invoke falls through to the catalog.
- The dedicated commands (`repos`, `repos pr`, `boards work-item`, `devops team`, the connection-data and identity lookups). They predate this rule and have no written reason; [#17](https://github.com/toolspell/tfsaz/issues/17) lists them. Moving them is not free: T3 allows 30 seconds per read, and an uncached catalog adds a large response to each call.
- `az rest` takes the caller's URL by design and resolves nothing.

## Target API version 7.1

- TFSaz's own requests send `api-version=7.1`, or a `7.1-preview.N` variant where the resource is only a preview in 7.1 (policy evaluations `7.1-preview.1`, work-item comments `7.1-preview.4`). T3 Code sends the same versions. The two lookups below 7.1 are listed at the end of this section.
- [Microsoft's version table](https://learn.microsoft.com/en-us/rest/api/azure/devops/#api-and-tfs-version-mapping) lists 7.1 for Azure DevOps Server 2022.1 and later. TFSaz is live-proven on 2022.2 only ([live results](../verification/live-t3-2026-11.md)), so 2022.2 is the supported floor and earlier releases are unverified.
- Do not raise a request above 7.1 without a live check on the floor release, and do not lower one below it. A different version can change a response shape that T3 decodes.
- Where a version is part of the behavior TFSaz mirrors, the extension's value wins under the [compatibility contract](../../AGENTS.md#compatibility-contract): `devops invoke` defaults to `5.0` without `--api-version`, as the extension does; T3 always passes `7.1` or `7.1-preview`.
- The connection-data (`5.0-preview.1`) and identity (`5.0`) lookups in `client.go` copy the extension's SDK clients, but they are TFSaz's own requests, not a passthrough, so the 7.1 target applies. Moving them is tracked in [#18](https://github.com/toolspell/tfsaz/issues/18).

## What the server reports about versions

Nothing uses this today. It is here for when TFSaz needs to detect what a server supports, such as the version check in [#20](https://github.com/toolspell/tfsaz/issues/20).

| Source | What it tells |
| --- | --- |
| `OPTIONS _apis?allHostTypes=true` | Per resource: `minVersion`, `maxVersion`, `releasedVersion`, `resourceVersion`. The `maxVersion` of a long-lived resource such as `git/repositories` approximates the server's API level. `az devops invoke` without `--resource` lists it. |
| `_apis/connectionData` | `deploymentType` (hosted or on-premises), `instanceId` and `deploymentId`. With a `connectOptions` value that includes services, `locationServiceData.serviceDefinitions` carries the same version fields per service. Which value fills it is unverified. |
| `_apis/resourceAreas` | Area names and their host URLs, no versions. Empty on the server, per the extension's SDK. |
