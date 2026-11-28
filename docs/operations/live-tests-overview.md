# Live tests overview

The big picture of the live-test toolchain: what it is made of, how it runs and how people and agents use it. The [runbook](live-tests.md) has the commands, the [internals](live-tests-internals.md) how the bases are built and what the tool keeps on the host.

The environment has two parts: VMs as test clients and a Docker test bed as the server side. Downloads are cached and the VM bases are built once; all worktrees share them. Each worktree then gets its own instance: its own clones and its own services, subnet and ports. Tart runs the Ubuntu and macOS VMs, QEMU the Windows VM, Packer drives every base build, and Docker Compose runs the test bed.

## Where everything comes from

```mermaid
flowchart LR
  subgraph download["Downloaded once"]
    direction TB
    u1["Ubuntu Server ISO<br/>3 GB"]
    m1["Cirrus Labs macOS image<br/>29 GB, already installed"]
    w1["Windows 11 ISO, you download it<br/>+ virtio-win driver ISO"]
    d1["Docker Hub images<br/>golang, debian, alpine, coredns"]
  end
  subgraph base["Built once, shared"]
    direction TB
    u2["Ubuntu base<br/>install + GNOME, about 30 min"]
    m2["macOS base<br/>image adjusted, about 3 min"]
    w2["Windows base<br/>install, about 13 min"]
  end
  subgraph instance["Per worktree, thrown away"]
    direction TB
    u3["Ubuntu clone<br/>APFS copy, boots in about 20 s"]
    m3["macOS clone<br/>APFS copy, boots in about 30 s"]
    w3["Windows clone<br/>qcow2 overlay, boots in about 1 min"]
    d3["Test bed services<br/>mock and router built per instance,<br/>CoreDNS as is"]
  end
  u1 -- "Tart + Packer" --> u2 --> u3
  m1 -- "Tart + Packer" --> m2 --> m3
  w1 -- "QEMU + Packer" --> w2 --> w3
  d1 -- "docker compose up --build" --> d3
  classDef download fill:#1e3a8a,stroke:#3b82f6,color:#fff
  classDef base fill:#134e4a,stroke:#2dd4bf,color:#fff
  classDef instance fill:#713f12,stroke:#facc15,color:#fff
  class u1,m1,w1,d1 download
  class u2,m2,w2 base
  class u3,m3,w3,d3 instance
```

The base builds also install SSH access, WireGuard and Chrome. No step installs TFSaz; a test installs it in its clone. The test bed has no shared build: each instance builds its mock and router images from Docker's layer cache and removes them on `down`.

Details: [Base VMs](live-tests-internals.md#base-vms) and [Clones](live-tests.md#clones).

## How it runs

At runtime, the clones reach the test bed only through a WireGuard tunnel to the router, like clients on a company network. The host reaches each clone over SSH through the hypervisor's NAT, and `tools/live` reads the mock's request journal. The mock's ports listen on the host's loopback only; the router's WireGuard port listens on all host interfaces, so the NATed guests reach it. Traffic to anything outside the test bed leaves through Docker's NAT, so the clones have internet access for now.

```mermaid
flowchart TB
  host["Host: tools/live"]
  subgraph inst["Instance tfsaz-live-ID, subnet 172.30.(53+slot).0/24"]
    direction TB
    clients["Clones<br/>Ubuntu, macOS, Windows"]
    router["Router .2<br/>WireGuard, resolver for the VMs"]
    mock["Mock .10<br/>devops.mock.test: REST, Git<br/>llm.mock.test: text generator"]
    dns["DNS sinkhole .53<br/>mock.test names to the mock,<br/>others NXDOMAIN, logs queries"]
    clients -- "WireGuard tunnel" --> router
    router --> mock
    router --> dns
  end
  internet["Internet"]
  host -- "SSH through the hypervisor's NAT" --> clients
  host -- "request journal" --> mock
  router -- "Docker NAT, for now" --> internet
  classDef instance fill:#713f12,stroke:#facc15,color:#fff
  classDef bed fill:#4c1d95,stroke:#a78bfa,color:#fff
  class clients instance
  class router,mock,dns bed
```

Details: [Worktrees and instances](live-tests.md#worktrees-and-instances), [Network](live-tests.md#network) and [DNS sinkhole](live-tests.md#dns-sinkhole).

## How it is used

Everything runs through one tool, `go run ./tools/live`. Maintainers call it directly; agents find it through AGENTS.md, which points to the `live-tests` skill, and the skill to the [runbook](live-tests.md). Automated cases use the Go harness, which builds the tool once and drives it. GitHub CI and `ci-local` only run the unit tests of both: the tool allows only help and read-only commands when `CI` or `GITHUB_ACTIONS` is set, and the harness skips its VM tests.

```mermaid
flowchart TB
  subgraph who["Who starts it"]
    direction LR
    maintainer["Maintainer"]
    agent["Agent"]
  end
  subgraph guide["Instructions"]
    direction LR
    agentsmd["AGENTS.md<br/>where live tests live, what can go wrong"]
    skill["Skill live-tests<br/>choose guests, run a case"]
    runbook["Live tests runbook<br/>setup, commands, troubleshooting"]
  end
  subgraph tools["Interfaces"]
    direction LR
    harness["Go harness<br/>TFSAZ_LIVE=1 go test ./live-tests/harness"]
    cli["Tool go run ./tools/live<br/>setup, up, vm, mock, down; -help, -json"]
  end
  env["Environment<br/>VMs and test bed"]
  ci["GitHub CI and ci-local<br/>unit tests only"]
  agent --> agentsmd --> skill --> runbook
  maintainer --> runbook
  skill --> harness
  runbook --> cli
  harness -- "builds once and calls" --> cli
  cli --> env
  ci -. "help and read-only commands only; harness skips" .-> cli
  classDef guide fill:#1e3a8a,stroke:#3b82f6,color:#fff
  classDef tool fill:#134e4a,stroke:#2dd4bf,color:#fff
  classDef env fill:#4c1d95,stroke:#a78bfa,color:#fff
  classDef blocked fill:#7f1d1d,stroke:#f87171,color:#fff
  class agentsmd,skill,runbook guide
  class harness,cli tool
  class env env
  class ci blocked
```

Details: [Write a live test](live-tests.md#write-a-live-test), [For agents](live-tests.md#for-agents) and the [`live-tests` skill](../../.agents/skills/live-tests/SKILL.md).

## What it covers today

The mock stands in for Azure DevOps Server 2022.2; checks against a real server use the shared testbed ([#33](https://github.com/toolspell/tfsaz/issues/33), [#47](https://github.com/toolspell/tfsaz/issues/47)). The smoke test runs on all three guests ([record](../verification/live-vm-smoke-2026-11.md)). Still open: the full test suite in each guest's desktop session ([#48](https://github.com/toolspell/tfsaz/issues/48)), browser trust on each OS ([#39](https://github.com/toolspell/tfsaz/issues/39)), external links from T3 on Windows ([#42](https://github.com/toolspell/tfsaz/issues/42)), offline runs and a fake agent for T3's agent threads.
