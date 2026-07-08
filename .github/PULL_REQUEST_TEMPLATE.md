## Summary

-
-

## Type

- [ ] Feature
- [ ] Bug fix
- [ ] Documentation
- [ ] CI/release
- [ ] Refactor/maintenance

## Verification

- [ ] `gofmt -l .`
- [ ] `go vet ./...`
- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `make build`
- [ ] GoReleaser config check, if release/build config changed

## Notes

- Security-sensitive changes reviewed for secrets, auth bypasses, and read-only guarantees.
- User-facing behaviour/docs updated where needed.
