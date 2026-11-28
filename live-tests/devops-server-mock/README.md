# Azure DevOps Server mock

A small stand-in for an Azure DevOps Server 2022.2 testbed, for live tests of TFSaz and stock T3 Code. It serves only what TFSaz and T3 call:

- the REST routes behind the `az` commands T3 runs (see [Azure CLI compatibility](../../docs/user/az-compatibility.md#commands-t3-calls)), at the API versions they send;
- the resource-location catalog TFSaz reads with `OPTIONS _apis?allHostTypes=true` to resolve `devops invoke` routes, recorded from a 2022.2 collection (`catalog.json`); the routes it resolves to for T3's calls are served;
- Git over HTTPS and HTTP (Basic authentication with the PAT, any user name) and over SSH (public key, any user name);
- plain HTML pages at the URLs a browser opens: repository, PR, commit, work item and build.

Collections work at both `/DefaultCollection` and `/tfs/DefaultCollection`. REST and web URLs and HTTP or HTTPS clone URLs use the request's scheme, host, port and collection path. SSH clone URLs use the request host and collection path with the SSH port. The catalog serves the recorded relative route templates and identifies its request URL in `Content-Location`. See the [live-test bed's network table](../../docs/operations/live-tests.md#network) for addresses and ports.

## Run

`go run ./tools/live services up` starts it with the rest of the test bed.

| Variable | Default | Meaning |
| --- | --- | --- |
| `MOCK_PAT` | `mock-pat` | The only PAT the mock accepts. Its permissions are not checked. |

On every start the mock generates a fresh CA and TLS leaf for `devops.mock.test`. The TLS private keys stay in memory. The mock writes the public CA certificate as `ca.pem` in the gitignored `keys/` directory. It also generates SSH keys and writes `client_ed25519`, `client_ed25519.pub` and `known_hosts` there. SSH accepts only that client key.

State lives in memory and in bare repositories inside the container. A restart starts from the seed with new keys and an empty journal. See the [request journal commands](../../docs/operations/live-tests.md#request-journal).

For a native run, build the mock from this directory. Set `MOCK_HTTP_ADDR`, `MOCK_HTTPS_ADDR`, `MOCK_HTTPS_ALT_ADDR`, `MOCK_SSH_ADDR` and `MOCK_ADMIN_ADDR` to loopback addresses with free ports. Set `MOCK_SSH_PORT` to the SSH port advertised in clone URLs, `MOCK_DATA` to a temporary data directory and `MOCK_KEYS_DIR` to an existing temporary directory for the keys and CA. Set `MOCK_PAT` to the value clients will use. Stop the process and remove the temporary directories when done.

## Seed

All names are invented. The PAT belongs to `MOCK\tester` (Test User). The other identities are Rita Reviewer, Oscar Other, Alice Archer and Ben Builder (`MOCK\rreviewer`, `MOCK\oother`, `MOCK\aarcher`, `MOCK\bbuilder`).

| Collection | Project | Repositories | PRs | Work items |
| --- | --- | --- | --- | --- |
| `DefaultCollection` | `Mock Project` | `api`, `web app` | 1 to 5 | 1 to 4 |
| `DefaultCollection` | `Platform` | `infra` | 6, 7 | 5 |
| `Second Collection` | `Demo Project` | `api`, `docs site` | 1, 2 | 1, 2 |

- PRs cover active, draft, completed (squash merged) and abandoned, with two or three reviewers each and every vote: approved, approved with suggestions, waiting, rejected and no vote, some required.
- Checks are policy evaluations: pipelines (`API CI`, `Web CI`, `Infra CI`, `Demo CI`, `Docs build`), a `code-coverage` status and a minimum-reviewers policy, in the states `approved`, `running`, `queued` and `rejected`.
- Work items link to PRs and have comments and tags.

IDs are derived from names, and seeded commits have fixed dates, so IDs and commit IDs are the same on every start.

## Examples

From the repository root with the mock running:

```sh
IP=127.0.0.1
keys="$PWD/live-tests/devops-server-mock/keys"
curl --cacert "$(go run ./tools/live mock ca)" --resolve "devops.mock.test:8443:$IP" \
  -u :mock-pat 'https://devops.mock.test:8443/tfs/DefaultCollection/_apis/projects?api-version=7.1'
curl -u :mock-pat "http://$IP:8080/DefaultCollection/_apis/git/pullrequests/1?api-version=7.1"
git clone "http://user:mock-pat@$IP:8080/DefaultCollection/Mock%20Project/_git/api"
GIT_SSH_COMMAND="ssh -i \"$keys/client_ed25519\" -o IdentitiesOnly=yes -o UserKnownHostsFile=\"$keys/known_hosts\"" \
  git clone "ssh://$IP:2222/Second%20Collection/Demo%20Project/_git/api"
```

## Known differences from the server

- Only the `api-version` values TFSaz and T3 send are accepted: `5.0`, `5.0-preview.1`, `7.1`, `7.1-preview`, `7.1-preview.1` and `7.1-preview.4`, in the query or, as `devops invoke` sends it, in the `Accept` header.
- WIQL understands the clauses stock T3 builds (project, title and tag `CONTAINS`, state `IN` and `NOT IN`, assignee and creator, the paging cursor, the order) and ignores others.
- A 401 offers `Bearer` and `Basic`, not `NTLM`.
- PRs have one iteration that follows the current branches. Completing a PR merges at once; auto-complete is only recorded.
- Fields that neither TFSaz nor T3 reads are left out, such as `lastMergeCommit` on active PRs, `commentVersionRef` and most system fields in work-item updates.
