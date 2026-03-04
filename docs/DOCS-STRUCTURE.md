# Project Documentation — Best Practices & Structure

> A complete reference for structuring docs in a new open source project. Designed for Kubernetes operators and Go-based infrastructure tools, but applies broadly to any OSS project.

---

## The Core Principle

Documentation lives **as close to the code as possible**. Every doc has one home, one owner, and one clear purpose. Readers should never have to guess where to look.

```
The right question: "If someone wants to X, where do they go?"
Every doc should answer exactly one version of that question.
```

---

## Complete Docs Directory Structure
> TODO: Create a `docs/reference` folder and documentation of CRD's Phase-2
```
rca-operator/
│
├── README.md                          ← project front door (see below)
├── CONTRIBUTING.md                    ← how to contribute code
├── CODE_OF_CONDUCT.md                 ← community behavior standards
├── SECURITY.md                        ← how to report vulnerabilities
├── CHANGELOG.md                       ← version history, human-readable
├── LICENSE                            ← license file (no extension)
│
├── docs/                              ← all long-form documentation
│   │
│   ├── index.md                       ← docs home / navigation guide
│   │
│   ├── getting-started/
│   │   ├── installation.md            ← helm, kubectl, from source
│   │   ├── quickstart.md              ← fastest path to value (5 min)
│   │   └── prerequisites.md           ← cluster version, tools needed
│   │
│   ├── concepts/
│   │   ├── architecture.md            ← system design, data flow
│   │   ├── incident-lifecycle.md      ← how incidents are created/resolved
│   │   ├── correlation-rules.md       ← how the correlator works
│   │   └── autonomy-levels.md         ← 0–3 explanation (Phase 3+)
│   │
│   ├── guides/
│   │   ├── configure-slack.md         ← step-by-step Slack setup
│   │   ├── configure-pagerduty.md     ← step-by-step PD setup
│   │   ├── write-custom-runbook.md    ← how to add your own runbook
│   │   ├── multi-namespace.md         ← watching multiple namespaces
│   │   └── production-checklist.md    ← what to verify before go-live
│   │
│   ├── reference/
│   │   ├── rcaagent-crd.md            ← full CRD field reference
│   │   ├── incidentreport-crd.md      ← full IncidentReport field reference
│   │   ├── metrics.md                 ← all Prometheus metrics exposed
│   │   ├── rbac.md                    ← RBAC permissions explained
│   │   └── cli.md                     ← operator flags and env vars
│   │
│   ├── development/
│   │   ├── local-setup.md             ← kind cluster, make run
│   │   ├── testing.md                 ← unit, integration, e2e guide
│   │   ├── architecture-decisions/    ← ADRs (see below)
│   │   │   ├── 0001-use-kubebuilder.md
│   │   │   ├── 0002-in-memory-ring-buffer.md
│   │   │   └── template.md
│   │   └── releasing.md               ← how to cut a release
│   │
│   └── phases/                        ← phased roadmap docs (this project)
│       ├── PHASE1.md                  ← ← your current doc lives here
│       ├── PHASE2.md
│       ├── PHASE3.md
│       └── PHASE4.md
│
├── config/
│   └── samples/
│       ├── rcaagent-minimal.yaml      ← simplest possible working config
│       ├── rcaagent-full.yaml         ← all fields documented inline
│       └── incidentreport-example.yaml
│
└── runbooks/                          ← operational runbooks (Phase 3+)
    ├── README.md                      ← what runbooks are, how to use them
    ├── crashloop.yaml
    ├── oom.yaml
    └── node-pressure.yaml
```

---

## File-by-File Purpose

### Root-Level Files (The Community Layer)

These files are **discovered automatically** by GitHub, npm, and other platforms. They must live at the root. Never put them in `docs/`.

| File | Purpose | Who Reads It |
|---|---|---|
| `README.md` | Project front door. What it is, why it matters, quick install, quick example. | Everyone — first touchpoint |
| `CONTRIBUTING.md` | How to set up locally, PR process, commit conventions, what makes a good issue | Contributors |
| `CODE_OF_CONDUCT.md` | Expected behavior in issues, PRs, Discord, etc. | Community members |
| `SECURITY.md` | How to privately disclose a vulnerability. No public CVE process here. | Security researchers |
| `CHANGELOG.md` | Human-readable history of what changed per version | Users upgrading versions |
| `LICENSE` | The legal license (no extension — `LICENSE`, not `LICENSE.md`) | Everyone / legal teams |

---

### README.md — The Front Door

The README has one job: **get someone from "what is this?" to "I see value" in under 2 minutes**. Structure it in this exact order:

```markdown
# Project Name — One-Line Value Proposition

[Badges: license, go version, kubernetes version, CI status]

## What Is It?                    ← 2–3 sentences max. No jargon.
## Why Does It Exist?             ← problem statement. Optional but powerful.
## Key Features                   ← 4–6 bullets or a table. Scannable.
## Architecture (optional)        ← ASCII diagram or image if it helps
## Quick Start                    ← working example in < 10 lines
## Documentation                  ← link to docs/
## Contributing                   ← link to CONTRIBUTING.md
## License                        ← one line + link
```

**Rules for a good README:**
- The Quick Start must actually work, end-to-end, every time
- No wall of text in the first screen — badges → value prop → features → code
- Link out to `docs/` for everything detailed; don't bloat the README
- Keep it honest — don't document features that don't exist yet

---

### docs/ — The Knowledge Base

Split into four zones. Each zone has a distinct reader intent:

| Zone | Folder | Reader Intent |
|---|---|---|
| **Getting Started** | `docs/getting-started/` | "I want to install and try it" |
| **Concepts** | `docs/concepts/` | "I want to understand how it works" |
| **Guides** | `docs/guides/` | "I want to do a specific thing" |
| **Reference** | `docs/reference/` | "I need to look up an exact field/value" |

This is the [Divio documentation system](https://documentation.divio.com/) — the most battle-tested framework for OSS docs. Don't mix zones. A concept page should never contain step-by-step instructions.

---

### CHANGELOG.md — Version History

Use [Keep a Changelog](https://keepachangelog.com) format. Strictly.

```markdown
# Changelog

## [Unreleased]

## [0.1.0] - 2025-03-01
### Added
- RCAAgent and IncidentReport CRDs
- Pod, Event, Node, Deployment watchers
- 5 built-in correlation rules
- Slack and PagerDuty notifications
- Helm chart

## [0.0.1] - 2025-01-15
### Added
- Initial project scaffolding
```

**Rules:**
- Every change goes under `[Unreleased]` during development
- On release, rename `[Unreleased]` to the version + date
- Sections: `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, `Security`
- Never say "various bug fixes" — be specific

---

### Architecture Decision Records (ADRs)

Store every significant technical decision in `docs/development/architecture-decisions/`. Use a sequential number prefix.

```markdown
# ADR-0001: Use kubebuilder as the operator framework

## Status
Accepted

## Context
We need an operator framework for Go. Options: kubebuilder, operator-sdk, controller-runtime directly.

## Decision
Use kubebuilder.

## Consequences
- Standard Makefile targets (make generate, make manifests, make run)
- CRD generation is automated
- Tightly coupled to kubebuilder version upgrades
```

ADRs are invaluable 6 months later when someone asks "why did we do it this way?"

---

### config/samples/ — Living Examples

Sample YAMLs are documentation. They must:

1. **Actually work** — test them in CI
2. **Be commented** — every non-obvious field gets an inline comment
3. **Come in two flavors** — minimal (just required fields) and full (all options)

```yaml
# rcaagent-minimal.yaml — the simplest working configuration
apiVersion: rca-operator.io/v1alpha1
kind: RCAAgent
metadata:
  name: rca-agent
  namespace: rca-operator-system
spec:
  watchNamespaces:
    - default
  notifications:
    slack:
      webhookSecretRef: slack-webhook   # kubectl create secret generic slack-webhook --from-literal=url=<your-url>
      channel: "#incidents"
```

---

## Docs Ownership Rules

```
Rule 1: One doc, one purpose.
        If you can't describe a doc's purpose in one sentence, split it.

Rule 2: Docs live next to what they describe.
        CRD field reference → docs/reference/rcaagent-crd.md
        E2E test guide     → docs/development/testing.md
        Sample config      → config/samples/

Rule 3: The README links out; it doesn't contain.
        Long explanations belong in docs/. The README links to them.

Rule 4: Phase docs are living documents.
        Mark sections [DONE], [IN PROGRESS], [DEFERRED] as the phase evolves.
        Don't delete deferred items — move them to the next phase doc.

Rule 5: Every external link in docs/ must be tested.
        Dead links erode trust faster than missing docs.
```

---

## Docs-as-Code: What to Automate

| Task | Tool | When to Set Up |
|---|---|---|
| Spell check | `cspell` or `typos` | Day 1 (GitHub Action) |
| Dead link checking | `lychee` or `markdown-link-check` | Day 1 (GitHub Action) |
| Docs site generation | MkDocs + Material theme, or Docusaurus | When you hit ~10 doc files |
| API reference generation | `gen-crd-api-reference-docs` | After CRDs stabilize |
| CHANGELOG automation | `git-cliff` or `conventional-commits` | Before first release |
| Sample YAML validation | `kubeconform` in CI | After first CRD ships |

### Minimal GitHub Action for Docs CI

```yaml
# .github/workflows/docs.yml
name: Docs CI

on: [push, pull_request]

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Check spelling
        uses: crate-ci/typos@master

      - name: Check links
        uses: lycheeverse/lychee-action@v1
        with:
          args: --verbose --no-progress './**/*.md'

      - name: Validate sample YAMLs
        run: |
          curl -sL https://github.com/yannh/kubeconform/releases/latest/download/kubeconform-linux-amd64.tar.gz | tar xz
          ./kubeconform config/samples/*.yaml
```

---

## What to Write First (Prioritized Order)

When starting from scratch, write docs in this order. Don't write the reference before the quickstart — no one will reach it.

```
Priority 1 — Before first commit
  ✅ README.md (skeleton — even 20 lines is fine)
  ✅ LICENSE
  ✅ CONTRIBUTING.md
  ✅ CODE_OF_CONDUCT.md (copy Contributor Covenant)

Priority 2 — Before first public release
  ✅ docs/getting-started/quickstart.md
  ✅ docs/getting-started/installation.md
  ✅ CHANGELOG.md (starting with [0.1.0])
  ✅ config/samples/rcaagent-minimal.yaml
  ✅ SECURITY.md

Priority 3 — Before community grows
  ✅ docs/concepts/architecture.md
  ✅ docs/reference/rcaagent-crd.md
  ✅ docs/reference/incidentreport-crd.md
  ✅ docs/development/local-setup.md
  ✅ docs/development/testing.md
  ✅ First ADR (your biggest architectural decision)

Priority 4 — When you have users
  ✅ docs/guides/ (task-specific how-tos)
  ✅ docs/reference/metrics.md
  ✅ Docs site (MkDocs or Docusaurus)
  ✅ CHANGELOG automation
```

---

## Common Mistakes to Avoid

**Docs in the wrong place**
Don't put installation instructions in the README when they're 40 lines long. Link to `docs/getting-started/installation.md`.

**Outdated samples**
If `config/samples/rcaagent-full.yaml` isn't tested in CI, it will rot. Test all samples.

**Missing CHANGELOG**
The most common omission. Users upgrading between versions have no way to know what changed. Start it on day 1.

**Single giant README**
A 1000-line README is a sign docs/haven't been created yet. After ~150 lines, start extracting into `docs/`.

**No ADRs**
Six months in, no one remembers why you chose an in-memory ring buffer over Redis. Write it down when you decide.

**Phases docs with no status**
A phase doc that still says "IN PLANNING" when the phase is half-done misleads contributors. Update status as you go.

---

## Quick Reference Card

```
New contributor asks...          Send them to...
─────────────────────────────────────────────────────
"What is this project?"          README.md
"How do I install it?"           docs/getting-started/installation.md
"How does it work internally?"   docs/concepts/architecture.md
"How do I configure Slack?"      docs/guides/configure-slack.md
"What does this CRD field do?"   docs/reference/rcaagent-crd.md
"How do I run tests locally?"    docs/development/testing.md
"What changed in v0.2?"          CHANGELOG.md
"Why was X designed this way?"   docs/development/architecture-decisions/
"How do I contribute a PR?"      CONTRIBUTING.md
"I found a security bug"         SECURITY.md
"What's coming in Phase 2?"      docs/phases/PHASE2.md
```
