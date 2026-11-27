# Live VM browser results, 27 November 2026

Fresh bases and clones ran against the [live-test mock](../operations/live-tests.md) on an Apple Silicon host. Sizes are allocated on-disk sizes from `du -sh`.

| Guest | Base rebuild | Base size | `verify` | Forwarded `/json/version` | Default browser in the mock journal |
| --- | --- | --- | --- | --- | --- |
| macOS | 3m 51s | 33 GiB | 6 passed | `Chrome/158.0.8127.89` | `Version/27.2 Safari/605.1.15` |
| Ubuntu | 13m 28s | 9.4 GiB | 6 passed | `Chrome/158.0.8127.88` | `Firefox/161.0` |
| Windows | 25m 30s | 14 GiB | 6 passed | `Chrome/158.0.8127.89`, `Edg/158.0.4370.45` | `Edg/158.0.0.0` |

Before rebuilding, the original Ubuntu base ran T3 Code `v0.1.13-nightly.20261125.5258` and `vm t3 start` printed `http://127.0.0.1:9222`. After rebuilding, T3 also printed `http://127.0.0.1:9444` with the shared CDP wait. Ubuntu's GIO still selected `firefox_firefox.desktop` for HTTPS and the installed Chrome source retained its scoped `signed-by` keyring.

The browsers exposed CDP 1.3 over host loopback forwards. On each OS the optional URL appeared in the CDP tab list on a second port. Each desktop's default browser requested `/DefaultCollection/_apis/connectionData` and received HTTP 401 without credentials. Windows initially showed the app picker with Edge listed under **Default app**. The request appeared after choosing **Just once**.

An earlier Ubuntu build hit the first-boot APT lock. On macOS, an earlier build hit Chrome's Finder metadata during strict signature verification, and the next clone hit a first-launch quarantine prompt. The successful runs used provisioning that waits for the first-boot APT jobs and verifies the macOS signature before clearing Chrome's quarantine.
