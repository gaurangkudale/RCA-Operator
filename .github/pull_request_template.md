## What does this PR do?
<!-- One paragraph summary -->

## Why?
<!-- Context and/or link to the issue -->

## How was it tested?
<!-- Unit tests / manual validation / E2E steps -->

### Commands run
```bash
make lint
make test
make build
# optional when relevant:
# make test-e2e
```

## Scope
- [ ] This PR is focused on one concern (feature, fix, docs, test, or chore)
- [ ] This PR is in scope for the current roadmap phase (`docs/phases/PHASE1.md`)

## Checklist
- [ ] PR title follows Conventional Commits format (`type(scope): short description`)
- [ ] `make lint` passes
- [ ] `make test` passes with no new failures
- [ ] `make build` compiles cleanly
- [ ] `CHANGELOG.md` updated under `[Unreleased]`
- [ ] New public functions have Go doc comments
- [ ] Docs updated (if applicable)
- [ ] CRD/API docs updated in `docs/reference/` when changing `api/v1alpha1/`

## Risk and rollout
<!-- Any risk, migration notes, or rollout considerations -->

## Breaking changes
- [ ] No breaking changes
- [ ] Yes (describe below)

## Related issue
Fixes #<issue>

## Notes for reviewers
<!-- Anything you want reviewers to focus on -->
