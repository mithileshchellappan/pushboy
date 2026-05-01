## Summary

Describe what changed and why.

## Verification

List the commands you ran and their results.

- [ ] `go mod tidy -diff`
- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] `go build ./cmd/pushboy`
- [ ] OpenAPI YAML parses
- [ ] Docker build or Compose smoke test, if deployment files changed

## API Or Docs Impact

- [ ] Updated `docs/openapi.yaml`, if API behavior changed
- [ ] Updated `README.md`, if user-facing workflow changed
- [ ] Added or updated tests for behavior changes

## Safety

- [ ] No secrets, device tokens, database URLs, production payloads, or real user data are included
- [ ] Security-sensitive changes have been called out clearly
- [ ] Known limitations are documented
