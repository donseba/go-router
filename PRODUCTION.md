# Production Readiness Checklist

## Done

- [x] Freeze router registration after the first request.
- [x] Preserve response writer unwrapping for router and middleware wrappers.
- [x] Add trusted proxy configuration for `RealIP`.
- [x] Add CI for tests, race tests, and `go vet`.
- [x] Add production-oriented examples and benchmarks.
- [x] Replace registration panics with an optional error-returning API.
- [x] Expand mounted router host edge-case tests.
- [x] Continue improving OpenAPI schema support.
- [x] Add route parameter helper functions.
- [x] Add JSON request/response helpers.
- [x] Harden CORS with more policy tests.

## Next

- [ ] Add more route table and diagnostics helpers.
- [ ] Add benchmarks against `chi`.
- [ ] Add more real application examples.
