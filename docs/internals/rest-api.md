# REST API

TFSaz calls the Azure DevOps Server REST API directly. Two rules hold for every request it builds: resolve the route through the server's resource catalog, and target API version 7.1.

## Resolve routes through the catalog

Azure DevOps Server advertises its REST resources. `OPTIONS <collection>/_apis?allHostTypes=true` returns one resource location per route, with its area, resource name, route template and version range (`minVersion`, `maxVersion`, `releasedVersion`, `resourceVersion`). The extension's SDK resolves every call it makes this way, by location ID, and its `devops invoke` resolves area and resource names against the same list. T3 relies on that: its PR and Boards adapters call `az devops invoke --area ... --resource ...` and never build a URL.

A hard-coded `_apis/...` path skips that contract. It breaks silently when a server moves a route or lacks a resource, and the user gets an HTTP 404 instead of "absent from the collection catalog". So:

- A new REST call resolves its route through the catalog, or reuses a path that is already catalog-resolved.
- A hard-coded route needs a written reason in a comment at the call site. "It is shorter" is not one.
- The existing hard-coded routes are listed in [#17](https://github.com/toolspell/tfsaz/issues/17), with their reasons where one exists.

How TFSaz does it, in `internal/az/catalog.go` and `apiCall.invoke` in `api_call.go`:

- A catalog-resolved request goes through `Client.withCatalogRoute`, which takes the HTTP method, the resource and a function that sends the request on the resolved path and version.
- `fetchCatalog` sends the OPTIONS request to the profile's real collection URL. The response is large (a live 2022.2 collection advertised 1,198 locations) and each `az` call is a new process, so TFSaz caches it on disk in `<config>.cache`, one file per collection named after a hash of the real collection URL. The file is the response body as the server sent it: no PAT, header or other credential.
- An entry lives 12 hours from its modification time, the `DEFAULT_MAX_AGE` of the on-disk OPTIONS cache in the extension's SDK (`devops_sdk/_file_cache.py`). A future-dated entry counts as expired.
- A server upgrade takes effect before that: a cached catalog that cannot route a request is reloaded once, and a read (GET, HEAD, OPTIONS) that fails with HTTP 404 through a cached route reloads it once and repeats only if the route changed. A write is never repeated, as in [#13](https://github.com/toolspell/tfsaz/issues/13); its 404 only deletes the entry, so the next call fetches.
- Several `az` processes run at once, so an entry is replaced through a temp file and a rename. A missing, corrupt or unwritable entry only means a fresh fetch; the cache never fails a command.
- TFSaz does not check who owns the cache. A cached route still passes `bindResourceRoute` and the collection boundary, so an altered entry cannot send a request outside the collection, and whoever can write beside the configuration can already change its `collectionUrl`.
- `az devops invoke` without `--resource` always fetches and rewrites the entry: a listing shows what the server advertises now, as the extension's does (it fetches the `allHostTypes=true` catalog per call and keeps it only in memory).
- The [version check](#check-the-servers-api-version) also always fetches: it reports on the live server, and a cached catalog could still show the API level from before an upgrade. Its clients have no cache directory, so it leaves the entry alone.
- Only the command dispatcher in `cli.go` sets the cache directory (`Client.catalogCache`). A `Client` without it fetches the catalog once per process.
- TFSaz skips the extension's `_apis/resourceAreas` lookup, which picks the host per area. On Azure DevOps Server every area lives on the collection, and the extension's SDK notes that the server returns an empty list there.
- `resolveResource` matches area and resource case-insensitively, skips locations whose `minVersion` is above the requested version, and binds the route template. Of the matches it keeps the highest `maxVersion`, then the one that omits the fewest optional slots; a remaining tie is an error, not a guess.
- `bindResourceRoute` rejects values that could become path control segments (dot segments, `_apis`, `;`, slashes, also percent-encoded, trailing dots), so a catalog-resolved path stays inside the collection.
- Version negotiation follows the extension's SDK (`_negotiate_request_version` in `devops_sdk/client.py`): a version above `maxVersion` drops to `maxVersion`, with `-preview` when `releasedVersion` is lower, and a preview revision above `resourceVersion` drops to it. The negotiated version goes into the `Accept` header (`application/json;api-version=...`), as the SDK sends it.

Where TFSaz builds routes itself today:

- T3's exact `devops invoke` shapes for Git and work items (`parseStockInvoke` in `invoke.go`, `parseWitInvoke` in `workitems.go`). The reason is written in `cli.go`: TFSaz rewrites their responses (file contents, blob archives, workflow states, work-item links, comment cursors). Any other invoke falls through to the catalog.
- The dedicated commands (`repos`, `repos pr`, `boards work-item`, `devops team`, the connection-data and identity lookups). They predate this rule and have no written reason; [#17](https://github.com/toolspell/tfsaz/issues/17) lists them. With the cache, moving them adds a catalog fetch only when an entry is missing, expired or stale, which still has to fit T3's 30 seconds per read.
- `az rest` takes the caller's URL by design and resolves nothing.

## Target API version 7.1

- TFSaz's own requests send `api-version=7.1`, or a `7.1-preview.N` variant where the resource is only a preview in 7.1 (policy evaluations `7.1-preview.1`, work-item comments `7.1-preview.4`). T3 Code sends the same versions. The two lookups below 7.1 are listed at the end of this section.
- [Microsoft's version table](https://learn.microsoft.com/en-us/rest/api/azure/devops/#api-and-tfs-version-mapping) lists 7.1 for Azure DevOps Server 2022.1 and later. TFSaz is live-proven on 2022.2 only ([live results](../verification/live-t3-2026-11.md)), so 2022.2 is the supported floor and earlier releases are unverified.
- Do not raise a request above 7.1 without a live check on the floor release, and do not lower one below it. A different version can change a response shape that T3 decodes.
- Where a version is part of the behavior TFSaz mirrors, the extension's value wins under the [compatibility contract](../../AGENTS.md#compatibility-contract): `devops invoke` defaults to `5.0` without `--api-version`, as the extension does; T3 always passes `7.1` or `7.1-preview`.
- The connection-data (`5.0-preview.1`) and identity (`5.0`) lookups in `client.go` copy the extension's SDK clients, but they are TFSaz's own requests, not a passthrough, so the 7.1 target applies. Moving them is tracked in [#18](https://github.com/toolspell/tfsaz/issues/18).

## Check the server's API version

`init`, `profile add`, `devops login`, `repo verify` and `doctor` compare the collection with a floor of API 7.1, previews included (`checkAPIFloor` in `catalog.go`). That is the lowest version T3's adapter sends: `7.1`, `7.1-preview` and `7.1-preview.N`.

- **Source.** The highest `maxVersion` of the `git/repositories` locations in the catalog stands for the server's API level. Every release T3 can use has that resource, and it follows the server's version. A `maxVersion` of 7.1 passes even where 7.1 is still a preview (`releasedVersion` below it).
- **Severity.** `repo verify` and `doctor` report a server below the floor, or a catalog they cannot read, as an error and exit nonzero. `init`, `profile add` and `devops login` print a `WARNING:` line on stderr and still save the profile, because the PAT itself is valid. `repo verify` skips the check with a warning when the profile has no PAT; its Git checks need none.
- **`devops login` stderr.** The extra line breaks no caller: T3 never runs `devops login`, it only names it in its sign-in hint, and the extension's own `devops login` writes `WARNING:` lines to stderr too.

Sources not used:

| Source | Why not |
| --- | --- |
| `_apis/connectionData` | Reports `deploymentType`, `instanceId` and `deploymentId`. Versions per service appear in `locationServiceData.serviceDefinitions` only with a `connectOptions` value that includes services; which value fills it is unverified. |
| `_apis/resourceAreas` | Area names and their host URLs, no versions. Empty on the server, per the extension's SDK. |
