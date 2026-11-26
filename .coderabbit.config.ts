// Adapted from Toolspell's .coderabbit.config.ts (MIT), itself adapted from T3 Code's.
// CodeRabbit evaluates this file on its side; the import needs no local install.
import { defineConfig, type CodeRabbitContext } from "@coderabbitai/config";

const UNGATED_AUTHORS = new Set(["OWNER", "MEMBER", "COLLABORATOR"]);
const isGated = ({ pr }: CodeRabbitContext) => !UNGATED_AUTHORS.has(pr?.authorAssociation ?? "");

// The enforcement points SECURITY.md lists under "What TFSaz protects"; change both together.
const CREDENTIAL_PATHS = [
  "internal/az/{credentials,client,rest_url,api_call,cli,repo_commands,trace}.go",
  "internal/az/{preserve_git,https_git}.go",
];
const CREDENTIAL_DOCS = ["SECURITY.md", "docs/internals/architecture.md"];

// The commands T3 calls, by the file prefixes in AGENTS.md "Where code lives".
const T3_COMMAND_PATHS = "internal/az/{cli,repos,pr_writes,write,workitems,boards_write,teams}.go";
const REST_PATHS = "internal/az/{invoke,catalog,rest_url,generic_http,api_*}.go";
// Code that changes state outside TFSaz's configuration, as SECURITY.md lists it.
const SYSTEM_PATHS = "internal/az/{setup_*,preserve_*,repo_commands,route_*,https_*,link_*,browser_target,shim*}.go";
const PLATFORM_PATHS = "**/*_{darwin,linux,windows,unix,other}.go";

// A summary of SECURITY.md; change both together.
const SECURITY_RISK = `Rate security risk by the trust model in SECURITY.md. A change is risky when it could let a finding cross one of its boundaries:
1. A PAT reaches anything but its profile's real collection URL: an Azure-style alias or an absolute az rest --url not mapped first, a nested collection, or an Authorization header set through --headers.
2. A PAT appears in a file, configuration, command argument, log, trace output or fixture, or is stored anywhere but the OS keyring through go-keyring.
3. An authenticated request follows a redirect, or TLS verification is disabled on an authenticated connection.
4. A failed PAT in one collection selects another collection's identity.
5. git-remote-tfsaz or the route run Git HTTPS helper reads the keyring.
6. The redirector serves or forwards something it should refuse.
7. A command changes more than "What TFSaz changes on the machine" lists, or a system change lacks an undo or a check.
Intended, not a risk: hosts entries, local trust for a leaf certificate, a loopback listener on port 443, the Linux nftables redirect, link handlers, and the alias-key.pem exception. Ask for SECURITY.md and the user docs to describe such a feature, never for its removal.`;

const MERGE_RISK = `Rate merge risk higher when a change touches the constraints in AGENTS.md and docs/internals:
- Stock T3 Code could tell TFSaz apart from Microsoft's Azure CLI: changed arguments, JSON shapes, stderr or exit codes of a command T3 calls, or Azure-style aliases and synthetic URLs that T3's host recognition no longer accepts (docs/internals/t3-compatibility.md).
- A REST route hard-coded as an _apis path instead of resolved through the resource-location catalog, or an API version that Azure DevOps Server 2022.2 does not serve (docs/internals/rest-api.md).
- A system change that rerunning the command after an interruption cannot finish or undo (docs/internals/architecture.md, "System edits must be recoverable").
- A feature on fewer than macOS, Windows and Linux without "not supported on this platform" handling and a platform:<os> issue per missing platform.`;

export default defineConfig((ctx) => ({
  reviews: {
    high_level_summary: false,
    review_status: false,
    request_changes_workflow: false,
    allow_author_approval: !isGated(ctx),
    security_risk_instructions: SECURITY_RISK,
    merge_risk_instructions: MERGE_RISK,
    // Labels come from .github/labels.json and the workflows; there is one maintainer.
    suggested_labels: false,
    suggested_reviewers: false,
    auto_review: {
      enabled: true,
      // The layers of a stacked PR target branches other than main.
      base_branches: [".*"],
    },
    // Reviews only: CodeRabbit offers none of the actions that write code to a branch or a PR.
    finishing_touches: {
      docstrings: { enabled: false },
      unit_tests: { enabled: false },
      simplify: { enabled: false },
      autofix: { enabled: false },
      fix_ci: { enabled: false },
      resolve_merge_conflict: { enabled: false },
    },
    pre_merge_checks: {
      // The factory overload of defineConfig widens this literal to string otherwise.
      docstrings: { mode: "off" as const },
      override_requested_reviewers_only: isGated(ctx),
    },
    path_filters: ["!.reference-apps/**"],
    path_instructions: [
      {
        path: T3_COMMAND_PATHS,
        instructions:
          "Hold changed code to the compatibility contract in AGENTS.md and docs/internals/t3-compatibility.md: stock T3 must not be able to tell TFSaz apart from Microsoft's Azure CLI with the azure-devops extension.",
      },
      {
        path: REST_PATHS,
        instructions: "Hold changed code to the rules in docs/internals/rest-api.md.",
      },
      ...CREDENTIAL_PATHS.map((path) => ({
        path,
        instructions: `Hold changes to the credential rules in AGENTS.md and the trust model in ${CREDENTIAL_DOCS.join(" and ")}.`,
      })),
      {
        path: SYSTEM_PATHS,
        instructions:
          'Hold changed code to "Reversible and verifiable system changes" in AGENTS.md, "System edits must be recoverable" in docs/internals/architecture.md, and the table "What TFSaz changes on the machine" in SECURITY.md. Security-affecting features are intended: ask for documentation, not removal.',
      },
      {
        path: PLATFORM_PATHS,
        instructions:
          'Hold changed code to "Platform parity" in AGENTS.md: a feature missing on a platform needs "not supported on this platform" handling there and a `platform:<os>` issue.',
      },
      {
        path: "**/*_test.go",
        instructions:
          "Hold changed tests to the rules in docs/internals/testing.md. Request tests only as those rules allow.",
      },
      {
        path: "**/*.md",
        instructions:
          'Hold changed docs to "Documentation rules" in AGENTS.md and the names in docs/internals/naming.md.',
      },
    ],
  },
  // Outsiders could spend the hourly review quota and teach learnings that shape every review.
  // Revisit when the repository opens for outside contributions.
  chat: {
    allow_non_org_members: false,
  },
  knowledge_base: {
    code_guidelines: {
      filePatterns: [
        { files: "docs/internals/t3-compatibility.md", applyTo: T3_COMMAND_PATHS },
        { files: "docs/internals/rest-api.md", applyTo: REST_PATHS },
        ...CREDENTIAL_DOCS.flatMap((files) => CREDENTIAL_PATHS.map((applyTo) => ({ files, applyTo }))),
        { files: "SECURITY.md", applyTo: SYSTEM_PATHS },
        { files: "docs/internals/architecture.md", applyTo: SYSTEM_PATHS },
        // Source changes too, so a request for tests follows the same rules.
        { files: "docs/internals/testing.md", applyTo: "**/*.go" },
        { files: "docs/internals/naming.md", applyTo: "**/*.md" },
      ],
    },
  },
}));
