# Security Policy

## Supported Versions

We release security fixes for the **latest minor version** only. If you are running an older version, please upgrade before reporting.

| Version | Supported |
|---|---|
| 0.1.x (latest) | ✅ Yes |
| < 0.1 | ❌ No — please upgrade |

---

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub Issues.**

If you believe you have found a security vulnerability in RCA Operator, please report it privately so we can address it before it is publicly known.

### How to report

Send an email to **security@rca-operator.tech** with the following information:

- **Summary** — a brief description of the vulnerability
- **Severity** — your assessment (Critical / High / Medium / Low)
- **Affected versions** — which versions are affected
- **Steps to reproduce** — a minimal, clear reproduction path
- **Impact** — what an attacker could achieve by exploiting this
- **Suggested fix** — optional, but appreciated

You can also use [GitHub's private vulnerability reporting](../../security/advisories/new) if you prefer to stay within GitHub.

### What to expect

| Timeline | What happens |
|---|---|
| Within 48 hours | We acknowledge receipt of your report |
| Within 7 days | We confirm whether the issue is a vulnerability and assign a severity |
| Within 30 days | We aim to have a fix ready for confirmed vulnerabilities |
| On release | We credit you in the security advisory (unless you prefer anonymity) |

We will keep you informed throughout the process. If you do not hear back within 48 hours, please follow up — our email may have missed you.

---

## Disclosure Policy

We follow **coordinated disclosure**:

1. You report privately to us.
2. We investigate and develop a fix.
3. We release the fix and publish a [GitHub Security Advisory](../../security/advisories).
4. You may then disclose publicly (we ask for a minimum of **30 days** from initial report, or on fix release — whichever comes first).

We will never ask you to delay disclosure indefinitely. If we cannot fix the issue within 90 days, we will coordinate with you on a reasonable disclosure timeline regardless.

---

## Scope

### In scope

- **RCA Operator controller** — anything in `cmd/` or `internal/`
- **CRD definitions** — `api/v1alpha1/`
- **Helm chart** — `charts/rca-operator/`
- **RBAC manifests** — `config/rbac/`
- **Official Docker images** — published at `ghcr.io/gaurangkudale/rca-operator`

### Out of scope

- Vulnerabilities in third-party dependencies (please report these upstream; we will bump the dependency on our end)
- Findings from automated scanners with no demonstrated impact
- Issues requiring physical access to infrastructure
- Social engineering of maintainers or contributors

---

## Security Design Notes

The RCA Operator is designed with a minimal-privilege model:

- **Read-only cluster access by default.** The operator's `ClusterRole` only requests `get`, `list`, and `watch` on pods, events, nodes, and deployments. It writes only to its own CRDs.
- **Secrets are never logged.** Notification credentials (Slack webhooks, PagerDuty keys) are read from Kubernetes Secrets at runtime and never appear in logs or events.
- **No network egress beyond configured endpoints.** The operator only makes outbound calls to the Slack webhook URL and PagerDuty API endpoint you configure. No telemetry is sent home.
- **No shell execution.** The operator does not exec into pods or run arbitrary commands.

If you believe any of these properties are violated, that is almost certainly a vulnerability — please report it.

---

## Known Limitations (Not Vulnerabilities)

- The in-memory ring buffer does not persist across restarts. If the operator restarts mid-incident, the correlator state is lost. This is a known trade-off, not a security issue.
- The operator does not currently validate the contents of Slack webhook URLs — it trusts that the secret you provide is correct.

---

*Thank you for helping keep RCA Operator and its users safe.*
