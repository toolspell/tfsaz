# Azure CLI compatibility

## Commands T3 calls

These are the `az` calls used by the stock T3 adapter at revision `c478fde9fe`. Use the `tfsaz` executable directly for manual calls, or `az` with the `az` shim first on PATH.

| Command | Behavior |
| --- | --- |
| `az --version` | Prints the tfsaz version before configuration or authentication. |
| `az account show --query user.name -o tsv` | Prints the first verified login in profile order. Fails if none verify. |
| `az account show --query user` | Returns the verified login for the selected collection as JSON. If connection data gives a bare account, tfsaz queries the same collection for its qualified login. |
| `az repos create --project P --name NAME` | Creates a Git repository in the selected project. |
| `az repos show` | Returns repository details, including clone URLs and default branch. |
| `az repos pr show --id N` | Returns the PR with its web link on the Azure-style alias. |
| `az repos pr list` | Returns a JSON array with T3's repository, branch, status, creator/reviewer and paging filters. |
| `az repos pr create` and `az repos pr update` | Create draft or ready PRs, edit title and description, change draft or lifecycle status, configure auto-complete, and complete with merge or squash. |
| `az repos pr reviewer add` and `remove` | Resolve reviewers within the collection and update the selected PR. |
| `az repos pr policy list --id N --top 100 --skip 0` | Returns one array of raw evaluations. T3 advances the offset and computes the check state. |
| `az devops invoke --area git --resource NAME` | Reads PR threads, labels, iterations, iteration changes, file revisions and batched blobs for the diff view. The supported Git resources are `pullRequestThreads`, `pullRequestLabels`, `pullRequestIterations`, `pullRequestIterationChanges`, `blobs` and `items`. |
| `az boards work-item create` and `update` | Create typed work items and edit title, description, state and named fields, including tags and assignee. |
| `az boards work-item show --id N --expand relations` | Reads a work item and the relations T3 uses to find linked PRs. |
| `az devops invoke --area wit --resource NAME` | Reads workflow states, searches with WIQL, loads result rows in batches, and reads comments, update history and tags (`workitemtypes`, `wiql`, `workitemsbatch`, `comments`, `updates`). T3 also uses catalog-resolved invoke to post work-item comments and add or remove comment reactions. |
| `az devops team list` and `az devops team list-member` | Lists project teams and members for T3’s assignee picker. |

Which T3 features these commands cover is in [What works in T3](t3-features.md).

## Checkout, output and errors

T3 also displays `az repos pr checkout --id N` for manual use. It fetches and switches to a same-repository PR branch through `origin` or the remote named by `--remote-name`. The remote and an explicit `--organization` must identify the same collection. The PR commands and dedicated Boards and team commands use JSON output. `devops invoke` and `rest` also support formatted and raw output, as described in [Request bodies, headers and output](#request-bodies-headers-and-output).

Use `devops login --organization URL` to set up the on-premises PAT, including when T3’s upstream diagnostic mentions Azure sign-in. An unsupported invocation exits with status **2**, leaves stdout empty, and reports the limitation on stderr. Configuration, authentication and server failures exit with status **1**.

## Select the collection

For API requests, an explicit `--organization` or `--org` selects the profile even from a checkout belonging to another collection. Outside a recognized checkout, supply the organization and any required project and repository. Work-item links use the selected Azure-style alias. WIQL queries and batch reads use HTTP POST to retrieve data.

```sh
az account show --query user -o json --org https://engineering-example.visualstudio.com
az repos pr list --org https://engineering-example.visualstudio.com \
  --project 'My Project' --repository 'My Repo' --status active --reviewer me --include-links --top 20
az repos pr policy list --org https://engineering-example.visualstudio.com --id 42 --top 100 --skip 0
az boards work-item show --org https://engineering-example.visualstudio.com --id 42 --expand relations
```

## Discover and call REST resources

From a configured checkout, list the selected collection’s live resource catalog with `az devops invoke`. Outside a checkout, add `--org` and the profile's Azure-style alias or collection URL. Each row includes the server’s area, resource name, route template and version range. Area and resource selectors are case-insensitive. JMESPath comparisons use the casing in the returned JSON.

```sh
az devops invoke --area build --query '[].[resourceName,routeTemplate]' -o tsv
az devops invoke --area core --resource projects --query 'value[].name' -o tsv
az devops invoke --area build --resource definitions --route-parameters 'project=My Project' --api-version 7.1
az rest --method get --url '/_apis/projects?api-version=7.1' --query 'value[].name' -o tsv
```

For generic `devops invoke` calls, tfsaz requests `OPTIONS _apis?allHostTypes=true` from the collection, resolves the advertised route, and negotiates the requested version (default 5.0). Supply placeholders with `--route-parameters key=value` and query values with `--query-parameters key=value`. Stock T3’s existing PR and Boards calls retain their specialized handling for file contents, workflow states and links. Supply IDs that occur between fixed route segments. If multiple templates still match, use `az rest` with the explicit collection API URL.

## Call REST directly

`az rest --url` accepts a configured collection’s real HTTPS URL, its Azure-style alias, or a collection-relative API path in a recognized checkout. tfsaz routes REST (`_apis`), Analytics (`_odata`) and Artifacts (`_packaging`) paths directly to the configured collection and refuses redirects. Both REST commands default to GET and accept explicit HEAD, OPTIONS, POST, PUT, PATCH and DELETE. Write requests operate with the selected PAT’s permissions.

### Request bodies, headers and output

Use `devops invoke --http-method POST --in-file request.json` or `rest --method POST --body @request.json` for JSON request bodies. For JSON Patch use `--media-type application/json-patch+json` with invoke, or `--headers Content-Type=application/json-patch+json` with rest. REST headers support Accept, Content-Type, conditional headers such as If-Match, Range and Prefer. Pass headers and URI parameters as `key=value` arguments, a JSON string dictionary or `@file`. Direct REST also accepts a text body and an explicit media type. JSON responses support `--query` and json, tsv, table or none output. Use `--out-file` with invoke or `--output-file` with rest to save a raw response to a new file. Direct REST also creates an empty output file for HEAD and HTTP 204. The DevOps extension’s invoke behavior creates a file only when the server supplies a content type.
