# Testing Guide

---

## Unit Tests

The project mixes two testing styles:

- **Older controller suite** uses **envtest** (real Kubernetes API server + etcd, no real cluster needed) wrapped with Ginkgo + Gomega. The entry point is `TestControllers` in `internal/controller/suite_test.go`.
- **Everything else** — including most newer controller tests — uses the **standard `testing` package** with table-driven tests and `sigs.k8s.io/controller-runtime/pkg/client/fake` for clients. New tests should follow this convention.

```bash
make test
```

Test files follow the pattern `*_test.go` in `internal/`.

## Run a Specific Test

```bash
# Standard testing package — by test function name
go test ./internal/correlator/... -v -run TestCorrelator_InjectedRuleFires

# Ginkgo entry point — focus by description (Ginkgo's own selector, not -run)
go test ./internal/controller/... -v -ginkgo.focus="RCAAgent reconciler"
```

## E2E Tests

E2E tests run against a real cluster (Kind is recommended). They build and deploy the operator image, apply CRs, and assert behaviour.

```bash
# Requires IMG to be set and accessible from the cluster
export IMG=<registry>/rca-operator:dev
make docker-build
kind load docker-image $IMG --name rca-dev

# Run e2e suite
make test-e2e
```

E2E test source is in `test/e2e/`.

## Pre-Release Install Verification

Before cutting a release, run the install verification script. It performs
Helm lint, renders the full/minimal/external-observability profiles, validates
them with kubeconform, installs the full profile into Kind, applies the
quickstart fixture, verifies an `IncidentReport`, and uninstalls everything.

```bash
scripts/verify-prerelease-install.sh

# Lint/template/kubeconform only
RUN_KIND_INSTALL=false scripts/verify-prerelease-install.sh
```

## Manual Scenario Testing

Use the fixtures in `test/fixtures/pods/` to trigger specific collector signals against a live operator. Most fixtures deploy into the `rca-demo` namespace — make sure your `RCAAgent` includes it in `watchNamespaces`.

```bash
# See README for the full scenario list
cat test/fixtures/README.md

# Example: trigger a CrashLoopBackOff incident
kubectl create namespace rca-demo  # if you haven't already
kubectl apply -f test/fixtures/pods/crashloop.yaml
kubectl get incidentreports -n rca-demo -w
```

For exit-code validation, use `test/fixtures/pods/exit-code.yaml`. The operator no longer creates a standalone `ExitCode` incident; instead, the resulting `CrashLoopBackOff` incident includes the classified exit-code context in its summary and timeline.

## Build and Push the Docker Image

```bash
export IMG=<your-registry>/rca-operator:latest
make docker-build docker-push IMG=$IMG
```

## Code Style

```bash
# Auto-fix lint issues before committing
make lint-fix
```
