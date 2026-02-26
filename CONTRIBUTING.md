# Contributing to RCA Operator

First off — thank you. RCA Operator is a community project and every contribution matters, whether it's a bug report, a docs fix, a new correlation rule, or a major feature.

This guide covers everything you need to go from "I want to help" to "my PR is merged."

---

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Ways to Contribute](#ways-to-contribute)
- [Before You Start — Check the Roadmap](#before-you-start--check-the-roadmap)
- [Local Development Setup](#local-development-setup)
- [Project Structure](#project-structure)
- [Making a Change](#making-a-change)
- [Commit Conventions](#commit-conventions)
- [Pull Request Process](#pull-request-process)
- [Writing Tests](#writing-tests)
- [Adding a Correlation Rule](#adding-a-correlation-rule)
- [Documentation Changes](#documentation-changes)
- [Issue Guidelines](#issue-guidelines)
- [Getting Help](#getting-help)

---

## Code of Conduct

This project follows our [Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to uphold it. Violations can be reported to **conduct@rca-operator.io**.

---

## Ways to Contribute

You don't have to write code to contribute meaningfully.

| Contribution | How |
|---|---|
| 🐛 Report a bug | [Open a Bug Report](../../issues/new?template=bug_report.md) |
| 💡 Propose a feature | [Open a Feature Request](../../issues/new?template=feature_request.md) |
| 📖 Improve docs | Edit any file in `docs/` and open a PR |
| 🧪 Add a test | Find an untested path and cover it |
| 🔧 Fix a bug | Pick an issue labeled `good first issue` or `bug` |
| 🔗 Add a correlation rule | See [Adding a Correlation Rule](#adding-a-correlation-rule) |
| 💬 Answer a question | Help out in [GitHub Discussions](../../discussions) |

---

## Before You Start — Check the Roadmap

Before building a significant feature, check:

1. **Is it in scope for the current phase?** See [`docs/phases/PHASE1.md`](docs/phases/PHASE1.md). If it's explicitly out of scope, it will be rejected — not because it's a bad idea, but because we're protecting the shipping schedule.
2. **Does an issue already exist?** Search [open issues](../../issues) first.
3. **For large changes** — open an issue and discuss the approach before writing code. This saves everyone time.

---

## Local Development Setup

### Prerequisites

| Tool | Version | Purpose |
|---|---|---|
| Go | 1.22+ | Build the operator |
| Docker | 24+ | Build images |
| kind | 0.23+ | Local Kubernetes cluster |
| kubectl | 1.28+ | Interact with cluster |
| kubebuilder | 3.14+ | Code generation |
| make | any | Run project commands |

### Step 1 — Clone the repo

```bash
git clone https://github.com/gaurangkudale/rca-operator.git
cd rca-operator
```

### Step 2 — Install dependencies

```bash
go mod download
```

### Step 3 — Create a local cluster

```bash
kind create cluster --name rca-dev
```

### Step 4 — Install CRDs

```bash
make manifests
make install
```

### Step 5 — Run the operator locally (outside the cluster)

```bash
make run
```

The operator will connect to your `kind` cluster using your local kubeconfig. You should see structured JSON logs in your terminal.

### Step 6 — Verify with a sample agent

```bash
kubectl apply -f config/samples/rcaagent-minimal.yaml
kubectl get rcaagent -n rca-operator-system
```

### Useful Make Targets

```bash
make generate          # regenerate DeepCopy methods after type changes
make manifests         # regenerate CRD YAMLs after type changes
make install           # install CRDs into current cluster
make uninstall         # remove CRDs from current cluster
make run               # run operator locally against current cluster
make build             # compile the binary
make test              # run unit tests
make test-e2e          # run E2E tests (requires kind cluster)
make lint              # run golangci-lint
make docker-build      # build Docker image
make helm-lint         # lint the Helm chart
```

---

## Project Structure

```
rca-operator/
├── api/v1alpha1/          ← CRD type definitions
├── internal/
│   ├── controller/        ← Kubernetes controllers (reconcile loops)
│   ├── watcher/           ← Pod, Event, Node, Deployment watchers
│   ├── correlator/        ← Ring buffer, correlation rules, incident model
│   ├── reporter/          ← Slack, PagerDuty, CR reporter
│   └── rca/               ← RCA engine (Phase 2+, stubs only in Phase 1)
├── config/
│   ├── crd/               ← Generated CRD manifests
│   ├── rbac/              ← RBAC rules
│   └── samples/           ← Example CRs
├── charts/rca-operator/   ← Helm chart
├── docs/                  ← All documentation
└── tests/e2e/             ← End-to-end tests
```

**Key design constraint:** watchers are read-only. They emit events into the correlator — they never write to the cluster. Only controllers and reporters write.

---

## Making a Change

### 1. Create a branch

```bash
git checkout -b fix/crashloop-threshold-off-by-one
# or
git checkout -b feat/add-statefulset-watcher
```

Branch naming:
- `feat/` — new feature
- `fix/` — bug fix
- `docs/` — documentation only
- `test/` — test only
- `chore/` — tooling, CI, dependencies

### 2. Make your changes

- Keep changes focused. One PR = one concern.
- If you change a CRD type, run `make generate && make manifests` before committing.
- If you add a new field to a CRD, document it in the relevant file under `docs/reference/`.

### 3. Run the checks locally

```bash
make lint       # must pass
make test       # must pass
make build      # must compile
```

### 4. Update CHANGELOG.md

Add an entry under `[Unreleased]`:

```markdown
## [Unreleased]
### Added
- StatefulSet watcher for pod disruption detection (#42)
```

---

## Commit Conventions

We use [Conventional Commits](https://www.conventionalcommits.org/).

```
<type>(<scope>): <short description>

[optional body]

[optional footer: Fixes #issue]
```

**Types:**

| Type | When to Use |
|---|---|
| `feat` | A new feature |
| `fix` | A bug fix |
| `docs` | Documentation only |
| `test` | Adding or fixing tests |
| `refactor` | Code change that isn't a feature or fix |
| `chore` | CI, tooling, dependencies |
| `perf` | Performance improvement |

**Scopes:** `watcher`, `correlator`, `reporter`, `controller`, `api`, `helm`, `ci`, `docs`

**Examples:**

```
feat(correlator): add ImagePullBackOff + registry rule
fix(watcher): correct OOMKilled exit code check from 143 to 137
docs(reference): add incidentRetentionDays field to RCAAgent CRD ref
test(correlator): add table-driven tests for node failure rule
chore(ci): upgrade golangci-lint to v1.57
```

**Rules:**
- Subject line ≤ 72 characters
- Use imperative mood ("add", not "added" or "adds")
- Reference the issue in the footer: `Fixes #42`

---

## Pull Request Process

### Before opening a PR

- [ ] `make lint` passes
- [ ] `make test` passes with no new failures
- [ ] `make build` compiles cleanly
- [ ] CHANGELOG.md updated under `[Unreleased]`
- [ ] New public functions have Go doc comments
- [ ] CRD changes have corresponding doc updates in `docs/reference/`

### PR title

Follow the same Conventional Commits format as your commit messages.

```
feat(watcher): add StatefulSet pod disruption detection
```

### PR description template

```markdown
## What does this PR do?
<!-- One paragraph summary -->

## Why?
<!-- Context or link to the issue -->

## How was it tested?
<!-- Unit tests / manual steps / E2E -->

## Checklist
- [ ] make lint passes
- [ ] make test passes
- [ ] CHANGELOG updated
- [ ] Docs updated (if applicable)

Fixes #<issue>
```

### Review process

- A maintainer will review within **5 business days**.
- At least **1 approval** is required to merge.
- For changes to `api/v1alpha1/`, **2 approvals** are required (CRD changes are hard to reverse).
- CI must be green before merge.
- We squash-merge PRs to keep the main branch history clean.

---

## Writing Tests

### Unit tests

Unit tests live next to the file they test: `correlator.go` → `correlator_test.go`.

Use table-driven tests for anything with multiple input/output cases:

```go
func TestCrashLoopOOMRule(t *testing.T) {
    tests := []struct {
        name     string
        signals  []Signal
        wantFire bool
        wantType IncidentType
    }{
        {
            name:     "crashloop + oom fires",
            signals:  []Signal{crashLoopSignal, oomSignal},
            wantFire: true,
            wantType: IncidentTypeOOM,
        },
        {
            name:     "crashloop alone does not fire",
            signals:  []Signal{crashLoopSignal},
            wantFire: false,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // ...
        })
    }
}
```

Use the [fake client](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client/fake) from controller-runtime for tests that need a Kubernetes client — never hit a real cluster in unit tests.

### E2E tests

E2E tests live in `tests/e2e/` and use a real `kind` cluster. They test the full path: CR applied → incident detected → notification sent.

```bash
make test-e2e
```

E2E tests are slower and are only run in CI on PRs to `main`. Don't gate your local development loop on them.

### Coverage target

We aim for **>80% coverage** on `internal/correlator/` and `internal/watcher/`. Check with:

```bash
make test-coverage
```

---

## Adding a Correlation Rule

Correlation rules are the core value of the project. Here's how to add one properly.

### 1. Define the rule in `internal/correlator/rules.go`

```go
// RuleRegistryCredentials fires when ImagePullBackOff is detected
// with no prior successful pull from the same image.
func RuleRegistryCredentials(signals []Signal) *IncidentCandidate {
    hasPullFailure := containsType(signals, SignalImagePullBackOff)
    hasNoPriorSuccess := !containsType(signals, SignalImagePullSuccess)
    if hasPullFailure && hasNoPriorSuccess {
        return &IncidentCandidate{
            Type:     IncidentTypeRegistry,
            Severity: P2,
            Signals:  filterByType(signals, SignalImagePullBackOff),
        }
    }
    return nil
}
```

### 2. Register it in the rule set

```go
var DefaultRules = []Rule{
    RuleCrashLoopOOM,
    RuleBadDeploy,
    RuleNodeLevel,
    RuleRegistryCredentials, // ← add here
    RuleNodeFailure,
}
```

### 3. Write table-driven unit tests

Cover: fires correctly, does not fire on partial signals, does not double-fire within cool-down window.

### 4. Document it

Add a row to the correlation rules table in `docs/concepts/correlation-rules.md`.

### 5. Update CHANGELOG.md

```markdown
### Added
- Correlation rule: ImagePullBackOff + no prior success → Registry credentials incident (#55)
```

---

## Documentation Changes

Docs are first-class contributions. The bar is the same as code.

- Docs live in `docs/` — see [`DOCS-STRUCTURE.md`](DOCS-STRUCTURE.md) for where each type of doc belongs.
- Don't add long explanations to `README.md` — link to `docs/` instead.
- If you change a CRD field, update `docs/reference/rcaagent-crd.md` or `docs/reference/incidentreport-crd.md` in the same PR.
- All links in `docs/` are checked by CI (`lychee`). Don't add dead links.

---

## Issue Guidelines

### Bug reports

Please include:
- RCA Operator version (`kubectl get rcaagent -o yaml | grep version`)
- Kubernetes version (`kubectl version`)
- What you expected to happen
- What actually happened
- Relevant logs (`kubectl logs -n rca-operator-system deploy/rca-operator-controller-manager`)
- The `RCAAgent` CR you're using (redact any secrets)

### Feature requests

Please include:
- The problem you're trying to solve (not just the solution)
- Which phase/roadmap item this relates to, if any
- Whether you're willing to implement it

### Labels we use

| Label | Meaning |
|---|---|
| `good first issue` | Suitable for first-time contributors |
| `help wanted` | We'd welcome a community PR |
| `bug` | Confirmed defect |
| `enhancement` | New capability |
| `phase-1` / `phase-2` / ... | Roadmap phase this belongs to |
| `wont-fix` | Out of scope or intentional behaviour |
| `needs-info` | Waiting on more detail from reporter |

---

## Getting Help

- **GitHub Discussions** — for questions, ideas, and design conversations: [Discussions](../../discussions)
- **Issues** — for bugs and concrete feature requests only: [Issues](../../issues)
- **Security issues** — do not open a public issue. See [SECURITY.md](SECURITY.md).

---

*Thank you for making RCA Operator better.*
