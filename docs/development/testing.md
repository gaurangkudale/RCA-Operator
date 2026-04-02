# Testing Guide

---

## Unit Tests

The project uses two testing styles:

- **Controller tests** use **envtest** (real Kubernetes API server + etcd, no real cluster needed) with Ginkgo + Gomega. See `internal/controller/suite_test.go` for the envtest setup.
- **All other unit tests** use the **standard `testing` package** with table-driven tests. New tests should follow this convention.

```bash
make test
```

Test files follow the pattern `*_test.go` in `internal/`.

## Run a Specific Test

```bash
# By test function name
go test ./internal/correlator/... -v -run TestCorrelator_InjectedRuleFires

# Controller tests (Ginkgo)
go test ./internal/controller/... -v -run TestControllers/"RCAAgent reconciler"
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

## Manual Scenario Testing

Use the fixtures in `test/fixtures/pods/` to trigger specific collector signals against a live operator:

```bash
# See README for the full scenario list
cat test/fixtures/README.md

# Example: trigger a CrashLoopBackOff incident
kubectl apply -f test/fixtures/pods/crashloop.yaml
kubectl get incidentreports -n default -w
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
