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
- [x] Resolve the client IP from the right of X-Forwarded-For, trusting configured proxies only.
- [x] Validate incoming request IDs.
- [x] Log through `log/slog`; no printing from library code.
- [x] Recover without writing into started responses; pass `http.ErrAbortHandler` on.
- [x] Support `http.Flusher` and `http.Hijacker` through the response writer wrappers (`http.Hijacker` except `ContentLengthMiddleware`, and `Compress` once it compresses).
- [x] Fail on `Use` after routes; freeze mounted routers with their parent.
- [x] Fill rest wildcards and report missing or unknown parameters in reverse routing.
- [x] Keep the trailing slash in reverse paths (`/admin/{$}` is `/admin/`); export the `route` template function as `RoutePathArgs`.
- [x] Mounted routers take over routes, names, hosts and docs registered after `Mount`; their reverse routing gives public paths.
- [x] Per-route middleware with `Use` on a route.
- [x] `ServeFiles` and `ServeFile` return their route, so they can be named and get middleware.
- [x] Active routes respect path segments, fill parameters and take a request, URL or path; `hasRoute` and `routeOr` for optional routes.
- [x] Keep a response's own error page from `HandleStatus` with `NoIntercept`.
- [x] `UseAll` for middleware around every request, including plain 404 and 405 answers, redirects and `HandleStatus` handlers; `Use` covers routes, mounts and `NotFound`/`MethodNotAllowed` handlers; `As` returns the route.
- [x] `NotFound` and `MethodNotAllowed` for unmatched requests only, scoped by group and host, through mounted routers, with no cost for matched requests.
- [x] `Handle`, `HandleFunc` and `Mount` return their route; route labels; `Match` and `MatchedRoute` (opt-in with `TrackRoutes`, so the hot path stays allocation-free).
- [x] `Mount` options `NamePrefix` and `AbsolutePaths`; mounting a handler that wraps a router (`Router() *Router`).
- [x] Middleware: `Compress`, `ETag`, `RateLimit`/`NewLimiter`, `BodyLimit`, `NoStore`/`CacheControl`/`Headers`, `X-Robots-Tag` in `SecurityHeaders`; trusted-proxy aware `Scheme`, `Host`, `Origin`, `SameOriginReferer`; `router.LocalURL` against open redirects.
- [x] `Observe` hook with `ResponseInfo`; `Logger` with `OmitQuery` and `Attrs`, logging requests whose panic passes through; `LogHandler` adds request IDs to every record.
- [x] Mount-aware matching: `Match` and `MatchedRoute` report the mount (`RouteInfo.Mount`) with its labels for paths a mounted router has no route for; `Owns` for a path with any method.
- [x] `ServeAt` serves a router at a prefix chosen per request with mount semantics; `For(req)` gives its public paths for reverse routing.
- [x] A `MethodNotAllowed` handler keeps `Allow` only when it answers 405; `Handle` takes a method in its pattern; mounted routers redirect trailing slashes to the public path; the `NotFound` handler of an outer router runs its `Use` middleware once for a mounted router's request.
- [x] Reverse routing for links built outside a request: `At(prefix)`, `PathFunc` for code that only builds links, and `RouteInfo.Redirect` for a path the router redirects, matched where the redirect leads.
- [x] Label lookups `MatchLabel`, `OwnsLabel` and `MatchedLabel`; `TryLabel`, and `TryMount` returning the mount, the error-returning registration of labels and mounts.
- [x] `RoutePathArgs` reads key/value pairs without a map: one allocation per path, also through `For` and `At`.
- [x] `middleware.SameOrigin` for client-sent URLs such as `HX-Current-URL`, behind trusted proxies.
- [x] Pools with bounded memory: `Compress` shares up to `min(GOMAXPROCS, 16)` idle gzip writers per level per process; `ETag` drops body buffers beyond 256 KiB.
- [x] `NewCache`: an output cache with versions, a TTL, several Vary lists per key, an LRU bounded by bytes, one render per burst and variant, 304s from the stored `ETag` and stats.
- [x] Production Notes recommend `UseAll` for headers every response carries.
- [x] `ETag` takes the media types it tags (`Types`, `CacheableTypes`), by default the common text formats: HTML, text, CSS, JavaScript, JSON, XML, Atom and RSS.
- [x] Content-Security-Policy: `CSP` merges policies directive by directive (also `SecurityHeaders`), `AddCSPSource` adds hashes per response, `StrictCSP`, and the `CSPPolicy` builder.
- [x] `LastModified` answers `If-Modified-Since` after `If-None-Match` (RFC 9110, second precision, `GET` and `HEAD` only); the output cache answers both conditions from fresh entries only, and passes neither on to the handler.

## Next

- [ ] Add more route table and diagnostics helpers.
- [ ] Add benchmarks against `chi`.
- [ ] Add more real application examples.
