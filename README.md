<p align="center">
    <a href="https://docs.gowebthings.com/go-router">
        <img src="./assets/go-router-logo.png" alt="go-router" height="70">
    </a>
</p>

# go-router

[Documentation](https://docs.gowebthings.com/go-router) · Part of [go-webthings](https://gowebthings.com/components).

`go-router` is a lightweight router for Go websites and APIs. It builds on the standard library `net/http` `ServeMux` and adds the pieces most small and medium applications usually end up needing: route groups, middleware, named routes, host and subdomain routing, mounted routers, OpenAPI helpers, route walking, static files, and practical middleware.

It is designed for applications that want to stay close to `net/http` without giving up website/API ergonomics.

## Features

- Standard-library based routing with Go's method-aware `ServeMux` patterns.
- Route groups, middleware-only groups and per-route middleware.
- Exact host, subdomain, and wildcard subdomain routing.
- Named routes with reverse path and URL generation.
- Mounted routers that stay part of the router they are mounted on: route names, route walk, hosts and OpenAPI metadata, also when registered after `Mount`.
- Routers served at a prefix chosen per request (`ServeAt`), such as a module on a page whose address can change, with public paths for handlers, reverse routing (`For`, and `At` without a request) and `MatchedRoute`.
- Route walking and route table diagnostics.
- Static file and single-file serving, as named routes.
- Custom status handlers, with per-route and per-response opt-out, and `NotFound` and `MethodNotAllowed` handlers for unmatched requests only.
- Route lookup by method and path (`Match`), by path for any method (`Owns`), the route that served a request (`MatchedRoute`) and route labels (`MatchLabel`, `OwnsLabel`, `MatchedLabel`), with the mount for paths a mounted router has no route for, and paths the router redirects matched where the redirect leads.
- Middleware for routes and `NotFound` handlers (`Use`) and for every request (`UseAll`).
- Optional trailing slash redirects.
- OpenAPI metadata and schema helpers.
- JSON request/response helpers.
- Path parameter helper functions.
- Bundled middleware for request IDs, logging, request observation, recovery, timeouts, security and cache headers, CORS, real IP and request origin, gzip compression, ETags and `Last-Modified` checks, an output cache, rate limiting, body limits, content length and timing, logging through `log/slog`, with the standard library only.
- Registration freezes after the first request to avoid runtime mutation races.

## Install

```bash
go get github.com/donseba/go-router
```

`go-router` targets modern Go (its `go.mod` requires Go 1.23.1) and uses the standard library routing improvements introduced in Go 1.22.

## Quick Start

```go
package main

import (
    "fmt"
    "log"
    "net/http"
    "time"

    "github.com/donseba/go-router"
    "github.com/donseba/go-router/middleware"
)

func main() {
    r := router.New(http.NewServeMux(), "Example API", "1.0.0")

    r.Use(middleware.RequestID)
    r.Use(middleware.Logger())
    r.Use(middleware.Recover)
    r.Use(middleware.Timeout(10 * time.Second))

    r.Get("/", func(w http.ResponseWriter, req *http.Request) {
        _, _ = fmt.Fprintln(w, "hello")
    }).As("home")

    r.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
        id, err := router.IntParam(req, "id")
        if err != nil {
            _ = router.BadRequest(w, err)
            return
        }

        _ = router.JSON(w, http.StatusOK, map[string]any{
            "id": id,
        })
    }).As("users.show")

    log.Fatal(http.ListenAndServe(":8080", r))
}
```

## Websites

`New` takes a `ServeMux` and an OpenAPI title and version; OpenAPI documentation stays off until `UseOpenapiDocs(true)` (see [OpenAPI](#openapi)). Websites can use `NewSite`, which creates its own `ServeMux`, needs no title or version and keeps OpenAPI documentation off. Its options set the handlers for unmatched requests (see [Not Found and Method Not Allowed](#not-found-and-method-not-allowed)):

```go
r := router.NewSite(
    router.WithNotFound(http.HandlerFunc(notFound)),
    router.WithMethodNotAllowed(http.HandlerFunc(notFound)),
)
r.RedirectTrailingSlash(true)
```

The router logs warnings, for example for route paths that could not be completed, and debug messages, to `slog.Default()`.

## Routing

Routes are registered by HTTP method:

```go
r.Get("/users", listUsers)
r.Post("/users", createUser)
r.Get("/users/{id}", showUser)
r.Put("/users/{id}", updateUser)
r.Patch("/users/{id}", patchUser)
r.Delete("/users/{id}", deleteUser)
r.Head("/health", healthHead)
r.Options("/health", healthOptions)
```

Route patterns use the standard `http.ServeMux` syntax:

```go
r.Get("/users/{id}", handler)
r.Get("/exact/{$}", handler)
r.Get("/assets/", handler)
```

Inside handlers, use standard `req.PathValue` or the small helper functions:

```go
id := req.PathValue("id")

intID, err := router.IntParam(req, "id")
```

`Handle` and `HandleFunc` register a handler for a `ServeMux` pattern, with or without a method, and return the route like the method functions do, so it can be named, labelled and get route middleware:

```go
r.Handle("/theme/", http.StripPrefix("/theme/", themeFiles)).As("theme").Label("asset")
r.Handle("PURGE /cache/", purge) // any method, also one of your own
```

Available helpers:

- `Param`
- `IntParam`
- `Int64Param`
- `BoolParam`
- `Float64Param`

## Error-Returning Registration

The normal registration API panics on duplicate routes or invalid late registration. That is useful during startup because mistakes fail loudly.

For apps that prefer explicit errors, use the `Try` variants:

```go
route, err := r.TryGet("/users/{id}", showUser)
if err != nil {
    return err
}

if err := route.TryAs("users.show"); err != nil {
    return err
}
```

Available `Try` methods:

- `TryGet`, `TryHead`, `TryPost`, `TryPut`, `TryPatch`, `TryDelete`, `TryOptions`
- `TryHandle`, `TryHandleFunc`
- `TryMount` (with the options of `Mount`)
- `TryUse` on a route (see [Route middleware](#route-middleware))
- `TryAs`, `TryLabel`

Their errors are a `router.RouteError`, whose `Err` holds what the panicking variant panics with.

For route paths, `RoutePathArgs` returns an error where `RoutePathWithParams` gives an empty or incomplete path (see [Named Routes and URLs](#named-routes-and-urls)).

The registering ones return the route, or the mount, as their panicking counterparts do, so it can be named, labelled and get middleware; it is `nil` when there is an error. Code that registers routes for others, such as the sections of an admin, can collect the errors instead of recovering panics:

```go
var errs []error

mount, err := r.TryMount("/admin", admin, router.AbsolutePaths())
if err != nil {
    return err
}

errs = append(errs, mount.TryLabel("admin"))

route, err := r.TryGet("/admin/pages/{id}", editPage)
if err == nil {
    errs = append(errs, route.TryUse(requireEditor), route.TryAs("admin.pages.edit"))
}

return errors.Join(append(errs, err)...)
```

`TryHandle`, `TryHandleFunc` and `TryMount` return the route next to the error since v0.12.0; they returned only the error before.

## Groups

Use `Group` to share a path prefix:

```go
r.Group("/api", func(api *router.Router) {
    api.Get("/users", listUsers)
    api.Post("/users", createUser)
})
```

Middleware added inside a group only applies to routes registered through that group:

```go
r.Group("/admin", func(admin *router.Router) {
    admin.Use(adminOnly)
    admin.Get("/dashboard", dashboard)
})
```

Use `With` for middleware-only groups:

```go
r.With(func(private *router.Router) {
    private.Use(requireLogin)
    private.Get("/account", account)
})
```

Group-level OpenAPI metadata is inherited by child routes:

```go
r.Group("/api", func(api *router.Router) {
    api.UseDocs(router.Docs{
        Tags: []string{"api"},
    })

    api.Get("/users", listUsers, router.Docs{
        Summary: "List users",
    })
})
```

## Host and Subdomain Routing

Scope routes to an exact host:

```go
r.Host("admin.example.com", func(admin *router.Router) {
    admin.Get("/", adminHome)
})
```

Use `Subdomain` for named subdomains:

```go
r.Subdomain("app", "example.com", func(app *router.Router) {
    app.Get("/", appHome)
})
```

Use `Subdomain("*", ...)` for wildcard subdomains:

```go
r.Subdomain("*", "example.com", func(tenant *router.Router) {
    tenant.Get("/dashboard", tenantDashboard).As("tenant.dashboard")
})
```

`WildcardSubdomain("example.com", fn)` is the same as `Subdomain("*", "example.com", fn)`.

Exact hosts take precedence over wildcard hosts. The apex domain does not match a wildcard subdomain. Unknown hosts fall back to the default router.

## Named Routes and URLs

Name a route with `As`:

```go
r.Get("/users/{id}", showUser).As("users.show")
```

`As` returns the route, so calls chain: `r.Get("/users/{id}", showUser).As("users.show").Use(requireLogin)`.

Generate paths:

```go
path := r.RoutePathWithParams("users.show", map[string]any{
    "id": 123,
})
// "/users/123"
```

Parameter values are URL-escaped:

```go
r.RoutePathWithParams("files.show", map[string]any{
    "name": "hello world/a",
})
// "/files/hello%20world%2Fa"
```

Rest wildcards (`{name...}`) keep their slashes and escape each segment. The value is a string or a `[]string` of segments:

```go
r.Get("/docs/{path...}", showDoc).As("docs.show")

r.RoutePathWithParams("docs.show", map[string]any{
    "path": "guides/getting started",
})
// "/docs/guides/getting%20started"
```

Reverse paths are the registered pattern without `{$}`, so a trailing slash is kept: `/admin/{$}` and `/admin/` are both `/admin/`, `/{$}` is `/`, and `/users` and `/users/` stay different routes. `RouteInfo.Path` holds this path and `RouteInfo.Pattern` the registered pattern.

`RoutePathWithParams` returns `""` for an unknown route and leaves a parameter without a value as its placeholder, reporting both to `slog.Default()`. Use `RoutePathArgs`, which takes the same map, to get an error instead and to add a query string:

```go
path, err := r.RoutePathArgs("users.show", map[string]any{
    "id": 123,
}, url.Values{"tab": {"posts"}})
// "/users/123?tab=posts"
```

It returns an error wrapping `router.ErrUnknownRoute`, `router.ErrMissingParam` (no value, or an empty one for a `{name}` wildcard) or `router.ErrUnknownParam` (a parameter the route does not have).

Host routes can generate full URLs:

```go
r.Host("admin.example.com", func(admin *router.Router) {
    admin.Get("/users/{id}", showUser).As("admin.users.show")
})

r.RouteURL("admin.users.show", map[string]any{"id": 123}, "https")
// "https://admin.example.com/users/123"
```

Wildcard subdomain URLs use a `subdomain` or `tenant` parameter:

```go
r.RouteURL("tenant.dashboard", map[string]any{
    "subdomain": "acme",
}, "https")
// "https://acme.example.com/dashboard"
```

`RouteHost(name)` returns the host pattern of a route, such as `admin.example.com` or `*.example.com`, and `""` for a route without a host or an unknown route.

Template helpers are available through `FuncMap`:

```go
tmpl := template.New("page").Funcs(r.FuncMap())
```

`route` builds a path from key/value pairs, maps of parameters and `url.Values` for the query string. An unknown route or a missing or unknown parameter fails the template instead of rendering a broken link:

```html
<a href="{{ route "users.show" "id" .User.ID }}">Profile</a>
<a href="{{ route "search" .Query }}">Search</a>
<a href="{{ route "users.posts.show" .Params }}">Post</a>
```

The same function is available in Go as `RoutePathArgs`:

```go
path, err := r.RoutePathArgs("users.show", "id", 42, url.Values{"tab": {"posts"}})
// "/users/42?tab=posts"
```

Its method values, including those of `For(req)` and `At(prefix)`, are `router.PathFunc`s, so code that only builds links can take a `PathFunc` instead of a `*Router`.

Key/value pairs alone, and a single `map[string]any`, are read as they are, without building a map of parameters: a path costs one allocation, the string itself, also on a mounted router or one returned by `For` or `At`. Other arguments, such as `url.Values`, are combined into a map first.

Included template functions:

- `route`
- `hasRoute`, `routeOr`
- `routePath`
- `routePathWithParams`
- `routeHost`
- `routeURL`
- `isActiveRoute`
- `isActiveRouteExact`
- `isActiveRouteContains`

### Optional routes

A template can link to a route that may not exist, such as a route of an optional module, without an error or a warning. `hasRoute` reports whether a route exists, and `routeOr` gives the route's path or a fallback for an unknown route:

```html
{{ if hasRoute "news.index" }}<a href="{{ route "news.index" }}">News</a>{{ end }}
<a href="{{ routeOr "news.index" "/" }}">News</a>
<a href="{{ routeOr "news.show" "#" "id" .ID }}">Article</a>
```

`routeOr` takes the arguments of `route` after the fallback. Only an unknown route gives the fallback: a missing or unknown parameter of a route that exists still fails the template. In Go, use `HasRoute`.

### Active routes

`isActiveRoute` reports whether the current page is a route's path or below it, for navigation. It respects path segments: a route at `/admin/news` is active on `/admin/news` and `/admin/news/7/edit`, not on `/admin/newsletter`. A route whose path ends in a slash, such as `/admin/{$}`, is active on `/admin/` and below it. `isActiveRouteExact` leaves out the paths below the route:

```html
<a href="{{ route "admin.news" }}" {{ if isActiveRoute .Request "admin.news" }}aria-current="page"{{ end }}>News</a>
```

The current page is a `*url.URL`, a `url.URL`, an `*http.Request` (its URL) or a string with a path or URL, such as the page address an htmx request reports, so a template does not need a `*url.URL`. `isActiveRouteContains` takes the same.

The route's wildcards match any value, unless arguments, as for `route`, give theirs. Parameters left out still match any value, and a query string is ignored:

```html
{{ isActiveRoute .Request "users.show" }}          <!-- /users/7, /users/8/posts -->
{{ isActiveRoute .Request "users.show" "id" 7 }}   <!-- /users/7, /users/7/posts, not /users/8 -->
```

An unknown route is not active. Arguments that are not key value pairs or maps, and parameters the route does not have, fail the template. `IsActiveRoute` and `IsActiveRouteExact` are the Go functions.

## Mounting

Mount any `http.Handler` under a path:

```go
r.Mount("/debug", http.DefaultServeMux)
```

A handler that is not a `*router.Router` sees the path without the mount path, as with `http.StripPrefix`.

A mounted `*router.Router` becomes part of the router it is mounted on. Its routes, route names, hosts and OpenAPI paths and components show up there below the mount path, whether they were registered before or after `Mount`, until the first request freezes both:

```go
admin := router.NewSite()
r.Mount("/admin", admin)

admin.Get("/{$}", dashboard).As("admin.dashboard")
admin.Get("/users/{id}", showUser).As("admin.users.show")

r.RoutePath("admin.dashboard")                                     // "/admin/"
r.RoutePathWithParams("admin.users.show", map[string]any{"id": 1}) // "/admin/users/1"
admin.RoutePath("admin.dashboard")                                 // "/admin/"
```

Paths in a mounted router are public paths:

- Its handlers and middleware see the request URL as the outer router received it: `req.URL.Path` is `/admin/users/1`, not `/users/1`. Route patterns are matched below the mount path, and `req.PathValue` works as usual.
- Its reverse routing (`RoutePath`, `RoutePathWithParams`, `RoutePathArgs`, `RouteURL` and `FuncMap`) resolves through the outermost router it is mounted on, so it gives the same public paths, and can also build the paths of that router's other named routes. A router mounted more than once resolves through its first mount.
- `Routes`, `Walk` and `RouteTable` describe a router's own registrations: on the mounted router they are relative to it, on the router it is mounted on they are below the mount path.

A router can be mounted on several routers. Each of them resolves the mounted router's route names to its own mount path, but the mounted router itself resolves through its first mount only:

```go
first.Mount("/a", child)
second.Mount("/b", child)

child.RoutePath("child.home")  // "/a/"
second.RoutePath("child.home") // "/b/"
```

To build the paths of another mount, ask the router it is mounted on. A router whose routes have names cannot be mounted twice on the same router, since route names are unique within a router, unless each mount has its own `NamePrefix` (see [Name prefixes](#name-prefixes)).

The mount path and the mount path with a trailing slash both reach the mounted router's `/` or `/{$}` route. Top-level OpenAPI fields of the mounted router (tags, security, security schemes) are taken over when it is mounted. A router cannot be mounted on itself or on a router mounted on it.

A mounted router's trailing slash redirects (`RedirectTrailingSlash`) go to the public path, `/admin/users/` to `/admin/users`, and so do the redirects of its `ServeMux` to a route with a trailing slash: with a route `/users/` in a router mounted at `/admin`, `/admin/users` redirects to `/admin/users/`, not to `/users/`.

`Mount` returns the mount as a route: it can be named (`RoutePath` gives the mount path with a trailing slash), get route middleware, which runs for every request of the mount, and labels, which a mounted router's routes get as well (see [Matching routes](#matching-routes)):

```go
r.Mount("/admin", admin).Use(requireLogin).Label("admin")
```

### Name prefixes

A router mounted more than once, such as one router per instance of a module, would register the same route names twice. `NamePrefix` puts a prefix before its names, as the router it is mounted on sees them:

```go
comments := router.NewSite()
comments.Post("/post", post).As("post")
r.Mount("/_/comments", comments, router.NamePrefix("comments."))

r.RoutePath("comments.post")  // "/_/comments/post"
comments.RoutePath("post")    // "/_/comments/post": its own names first
comments.RoutePath("home")    // a name of r: the outer names without the prefix
```

### Routers with absolute paths and wrapped routers

A router whose patterns include the mount path already, such as one that registers its routes below a base path it is given, is mounted with `AbsolutePaths`. Requests at and below the mount path reach it unchanged, and its routes and names are taken over as they are:

```go
admin := router.NewSite()
admin.Group("/admin", func(routes *router.Router) {
    routes.Get("/{$}", dashboard).As("admin.dashboard")
})
admin.RoutePath("admin.dashboard") // "/admin/", before and after the mount

r.Mount("/admin", admin, router.AbsolutePaths())
```

A handler with a `Router() *router.Router` method, such as a type that wraps its router in middleware of its own, is mounted as that router: its routes, names, hosts and docs become part of the router it is mounted on, and requests are served through the handler, which sees the full request URL. When it passes the request on to its router, the router matches below the mount path:

```go
type Admin struct{ router *router.Router }

func (a *Admin) Router() *router.Router { return a.router }

func (a *Admin) ServeHTTP(w http.ResponseWriter, req *http.Request) {
    a.router.ServeHTTP(w, req.WithContext(withMessages(req.Context())))
}

r.Mount("/admin", admin)                         // routes relative to /admin
r.Mount("/admin", admin, router.AbsolutePaths()) // routes below /admin already
```

`AbsolutePaths` on a handler that is not a router passes it the full path instead of the path below the mount.

### Serving at a prefix per request

A router whose address is only known per request, such as the router of a module on a page of a CMS that editors can move, is not mounted: the handler that knows where it is serves the request with `ServeAt`, giving the public path of the router's root route:

```go
news := router.NewSite(router.WithNotFound(http.HandlerFunc(notFound)))
news.Get("/{$}", list).As("index")
news.Get("/{slug}", show).As("show")

site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
    page := pages.Resolve(req.URL.Path)        // the news page is at /en/news
    news.ServeAt(w, req, page.Path, router.NamePrefix("news."))
})).Label("public")
```

For that request the router is mounted at the prefix, as with `Mount`:

- It matches with the path below the prefix: `/en/news` and `/en/news/` reach `/{$}`, `/en/news/hello` reaches `/{slug}`. Requests outside the prefix get a 404.
- Its handlers, middleware and `NotFound` handlers see the request URL as the calling handler received it, the public URL, and `req.PathValue` works as usual.
- `MatchedRoute` gives its routes below the prefix, `/en/news/{slug}`, with the labels of the route that called `ServeAt` and the name prefix of `NamePrefix`, to the routers the request came through as well: metrics and access logs see the module's routes.

Reverse routing has no request, so `For(req)` gives the router for the request's prefix: `RoutePath`, `RoutePathArgs`, `RouteURL`, `HasRoute`, `IsActiveRoute`, `FuncMap` and their variants give public paths:

```go
func show(w http.ResponseWriter, req *http.Request) {
    routes := news.For(req)

    self, _ := routes.RoutePathArgs("show", "slug", req.PathValue("slug")) // "/en/news/hello"
    index := routes.RoutePath("index")                                     // "/en/news"
    tmpl := template.New("show").Funcs(routes.FuncMap())                    // public paths in templates
    // ...
}
```

The root route's path is the prefix as given: `ServeAt(w, req, "/en/news")` gives `/en/news`, without a trailing slash, so links and form actions to the page itself, such as a form that posts to `/{$}`, are not redirected to it; `ServeAt(w, req, "/en/")` gives `/en/`. For a request not served at a prefix, `For` returns the router itself. A router served with `ServeAt` should not also be mounted with `Mount`; `AbsolutePaths` does not apply.

Outside such a request, such as for a search index, a feed or a block on another page that lists the module's items, `At(prefix)` gives the same paths without a request: what `For` gives in a request served at that prefix, root route included.

```go
routes := news.At("/en/news")

routes.RoutePath("index")                       // "/en/news"
routes.RoutePathArgs("show", "slug", "hello")   // "/en/news/hello"
news.At("/en/").RoutePath("index")              // "/en/"
news.At("").RoutePath("index")                  // "/": an empty prefix is "/"
```

The prefix is used as given, by `ServeAt` and `At` alike: it is not escaped, while the parameters filled in below it are, so give it the way it goes into a link, without `{` or `}`. The router `At` returns is for reverse routing only and resolves the router's own route names. `At` costs two allocations; keep its router when building many links below the same prefix.

## Middleware

Middleware uses the standard shape:

```go
type Middleware func(http.Handler) http.Handler
```

Register middleware globally:

```go
r.Use(middleware.RequestID)
r.Use(middleware.Logger())
r.Use(middleware.Recover)
```

Middleware is applied to a route when the route is registered, so `Use` must come before the routes it is meant for. `Use` panics once routes were registered on that router or one of its groups. Use `Group`, `With` or route middleware for routes that need other middleware.

### Route middleware

`Use` on a route adds middleware to that route only, for example a permission check:

```go
r.Group("/admin", func(admin *router.Router) {
    admin.Use(requireLogin)
    admin.Post("/users/{id}/delete", deleteUser).Use(can("users.delete")).As("admin.users.delete")
})
```

Middleware runs from the outside in:

1. middleware of the router, then of its groups (`Use` on a router), in registration order;
2. route middleware, in the order given, earlier `Use` calls on the route first;
3. the handler.

Route middleware can be added until the first request; after that `Use` on a route panics and `TryUse` returns an error. `RouteInfo.Middlewares` in `Walk` lists both kinds.

### Middleware for every request

Middleware of `Use` on a router runs around the handlers registered on it and its groups after `Use`:

- its routes, and every request of a `Mount`, which is a route as well, so also the requests a mounted router answers without a route of its own, such as its 404s;
- the `NotFound` and `MethodNotAllowed` handlers set on the router or one of its groups, also those set before `Use`.

It does not run for what the router answers without a handler: its plain 404 and 405 answers when no `NotFound` or `MethodNotAllowed` handler applies, trailing slash redirects and the `ServeMux`'s own redirects (such as for unclean paths), nor for `HandleStatus` handlers and requests `UseAll` middleware answers itself. Route middleware (`Use` on a route) only runs for the requests of that route; `NotFound` handlers do not run inside it.

`UseAll` adds middleware around the whole router, for every request it serves:

```go
r.UseAll(middleware.RequestID, middleware.Logger(), middleware.Recover)
```

It is the same as wrapping the router, `r.UseAll(a, b)` serves as `a(b(r))`:

```go
handler := middleware.RequestID(middleware.Logger()(middleware.Recover(r)))
log.Fatal(http.ListenAndServe(":8080", handler))
```

`UseAll` middleware runs before the route is matched, so `req.PathValue` is empty and `Use` middleware has not run yet; the context it sets reaches the handlers. Unlike `Use`, it can be called after routes were registered, but only on the router itself, not on a group, `With` or host router (it panics there). A mounted router runs its `UseAll` middleware for the requests that reach it, with the request URL as the outer router received it.

The bundled middleware package includes:

- `RequestID`, `RequestIDWithOptions`
- `Logger`, `Observe`, `LogHandler`
- `Recover`, `RecoverWithOptions`
- `Timeout`
- `RealIP`, `RealIPWithOptions`, `TryRealIP`, and `Scheme`, `Host`, `Origin`, `SameOrigin`, `SameOriginReferer`
- `SecurityHeaders`, `NoStore`, `CacheControl`, `Headers`
- `CSP`, `AddCSPSource`, `StrictCSP`, and the policy builder `CSPPolicy` (`ParseCSP`, `SplitCSP`)
- `Compress`
- `ETag`, `CacheableTypes`
- `LastModified`
- `NewCache`, `CacheKey`
- `RateLimit`, `NewLimiter`
- `BodyLimit`
- `CORS`
- `ContentLengthMiddleware`
- `Timer`, `TimerWithOptions`

The middleware package uses the standard library only.

### Client IP

`RealIPWithOptions` finds the client IP behind trusted proxies:

```go
r.Use(middleware.RealIPWithOptions(middleware.RealIPOptions{
    TrustedProxies: []string{"127.0.0.1", "10.0.0.0/8", "::1/128"},
}))

ip := middleware.ClientIP(req)
```

Forwarding headers are only believed when the direct peer is a trusted proxy. `X-Forwarded-For` is then read from the right, skipping trusted proxies, so the client cannot spoof its address by sending its own header: the first address that is not a trusted proxy is the client. `X-Real-IP` and then `Forwarded` are used when `X-Forwarded-For` is absent.

- `ClientIP(req)` returns the client IP, or the peer's IP when the middleware did not run.
- `GetClientIP(ctx)` returns the stored client IP, or `""`.
- `FromTrustedProxy(req)` reports whether the peer is a trusted proxy, so other forwarding headers such as `X-Forwarded-Proto` and `X-Forwarded-Host` can be believed.

When the client differs from the peer, `RemoteAddr` becomes the client IP with port 0 (`203.0.113.10:0`) on the request the middleware passes on, so it still parses with `net.SplitHostPort`. The request itself is not changed: middleware outside it still sees the peer's address. Otherwise `RemoteAddr` is left alone.

`RealIP` trusts no proxies: the client IP is the peer address. `RealIPWithOptions` panics on an invalid trusted proxy; `TryRealIP` returns an error instead. `ParseTrustedProxies` parses a list of addresses and ranges the same way, for checks of your own.

### Request origin and redirects

The scheme and host a request arrived on follow the same trust: `Scheme(req)` is `https` for TLS requests, and `Host(req)` is `req.Host`, unless a trusted proxy reports others in `X-Forwarded-Proto` and `X-Forwarded-Host` (or `Forwarded`), of which the last entry counts. A forwarded host must be a valid host name or IP address. `Origin(req)` is both, such as `https://example.com`:

```go
http.SetCookie(w, &http.Cookie{Name: "vote", Value: id, Secure: middleware.Scheme(req) == "https"})
link := middleware.Origin(req) + r.RoutePath("newsletter.confirm")
```

`SameOrigin(req, u)` reports whether a URL the client sent, such as the page an htmx request was made on, is an `http` or `https` address on the host the request arrived on: `Host(req)`, compared without case, so behind a trusted proxy that rewrites `Host` it is the proxy's `X-Forwarded-Host` that counts. Schemes are not compared, since a proxy may not report the scheme:

```go
current, err := url.Parse(req.Header.Get("HX-Current-URL"))
if err == nil && middleware.SameOrigin(req, current) {
    // current.Path is a page of this site
}
```

`router.LocalURL` guards redirects to addresses from a form or query against open redirects: it accepts paths on the site only, not `//host`, other schemes, backslashes or line breaks, and returns the parsed URL, for a target that is used further, such as a path and query to set on another URL. `SameOriginReferer` returns the path and query of the `Referer` when it is an address on the host the request arrived on (`SameOrigin`):

```go
next := req.FormValue("next")
if _, ok := router.LocalURL(next); !ok {
    next = "/"
    if referer, ok := middleware.SameOriginReferer(req); ok {
        next = referer
    }
}
http.Redirect(w, req, next, http.StatusSeeOther)

page, ok := router.LocalURL(req.FormValue("path")) // "/en/about?x=1"
if ok {
    preview.Path, preview.RawQuery = page.Path, page.RawQuery
}
```

### Request IDs

`RequestID` keeps an incoming `X-Request-ID` when it is at most 64 letters, digits and `-_.:` characters, and generates a random ID otherwise. The ID is set on the response and stored in the context (`GetRequestID(ctx)`). `RequestIDWithOptions` changes the header and generator, and can put the ID into other contexts too:

```go
r.Use(middleware.RequestIDWithOptions(middleware.RequestIDOptions{
    WithContext: metrics.WithRequestID, // func(context.Context, string) context.Context
}))
```

`middleware.WithRequestID(ctx, id)` stores an ID for `GetRequestID` without the middleware, such as in tests or background jobs.

### Logging and recovery

`Logger`, `Timer` and `Recover` log through `log/slog`, by default `slog.Default()`:

```go
r.Use(middleware.Logger(middleware.LoggerOptions{Logger: logger}))
r.Use(middleware.RecoverWithOptions(middleware.RecoverOptions{Logger: logger}))
r.Use(middleware.TimerWithOptions(middleware.TimerOptions{Logger: logger}))
```

`Level` in `LoggerOptions` and `TimerOptions` sets the level of the records, by default Info.

`Logger` logs method, URI, status, size, duration, client IP and request ID, also for a request whose panic passes through it (with `panicked=true`, and status 500 when no status was written). `OmitQuery` logs the path without the query string, which may hold tokens, and `Attrs` adds attributes of your own, such as the route that served the request. `router.MatchedRoute` only knows that route when the router records it, with `TrackRoutes` (see [Matching Routes](#matching-routes)):

```go
r.TrackRoutes() // for router.MatchedRoute
r.UseAll(middleware.Logger(middleware.LoggerOptions{
    OmitQuery: true,
    Attrs: func(req *http.Request, info middleware.ResponseInfo) []slog.Attr {
        route, _ := router.MatchedRoute(req)
        return []slog.Attr{slog.String("route", route.Name)}
    },
}))
```

`Observe` is the hook underneath: it calls a function with the request and a `ResponseInfo` (status, bytes, duration, whether a panic passed through and the response headers) when a request is done, for metrics and for middleware that acts on answers, such as a list of the 404s search engines may index:

```go
r.Use(middleware.Observe(func(req *http.Request, info middleware.ResponseInfo) {
    if info.Status == http.StatusNotFound && info.Header.Get("X-Robots-Tag") == "" {
        missed(req.URL.Path)
    }
}))
```

`ResponseInfo.Header` is the response's own header map, as the handler left it: read it, do not change it. `LogHandler` wraps a `slog.Handler` so every record logged with a request's context carries its `request_id`, also records of code that knows nothing about requests:

```go
logger := slog.New(middleware.LogHandler(slog.NewJSONHandler(os.Stderr, nil)))
logger.InfoContext(req.Context(), "page saved") // ... "request_id": "..."
```

`Recover` logs the panic with its stack and writes a 500 if the response has not started. If it has, the connection is aborted with `http.ErrAbortHandler`, so clients and caches do not mistake a truncated response for a complete one. Panics with `http.ErrAbortHandler` are passed on untouched.

### Compression

`Compress` gzips responses for clients that accept it: text, JSON, JavaScript, XML, SVG and other compressible types (`CompressibleType`) of at least 1 KiB. Images, video, archives, WOFF fonts, server-sent events, responses that are encoded already or partial, `HEAD`, `Range` and `Upgrade` requests, and responses flushed before 1 KiB go out as they are. Responses that could be compressed say `Vary: Accept-Encoding`, and a strong `ETag` of a compressed response becomes weak.

```go
r.Use(middleware.Compress())
r.Use(middleware.Compress(middleware.CompressOptions{
    Enabled: func(req *http.Request) bool { return settings(req).Compress }, // per request
    MinSize: 2048,
    Level:   gzip.BestSpeed,
}))
```

`Types`, a `func(contentType string) bool`, replaces `CompressibleType` as the test of what is worth compressing. `AcceptsGzip(header)` reports whether an `Accept-Encoding` header allows gzip.

The gzip writers are reused: a writer holds about 0.8 MB at `gzip.BestSpeed` and 1.05 to 1.2 MB at the other levels, the default included (0.3 MB at `gzip.HuffmanOnly`), too much to make one per response. Every `Compress` of the process with the same `Level` shares one set of writers: a free list of up to `min(GOMAXPROCS, 16)` idle writers, which the garbage collector leaves alone, and a `sync.Pool` behind it for bursts. So a level in use keeps up to about 13 to 19 MB on a machine with 16 or more processors, however many `Compress` middlewares use it; a second level keeps its own. Creating a `Compress` for a route costs no writer, but keep to one or two levels.

### ETags

`ETag` gives complete responses to `GET` and `HEAD` a weak `ETag`, a hash of the body, and answers `If-None-Match` with `304 Not Modified`. It does so for responses that every visitor may see alike: status 200, no cookie, no `ETag` or `Content-Encoding` of their own, not private or no-store, and a media type of `Types`. `Types` defaults to `DefaultETagTypes`: HTML, plain text, CSS, JavaScript, JSON, XML, Atom and RSS (`text/html`, `text/plain`, `text/css`, `text/javascript`, `text/xml`, `application/javascript`, `application/json`, `application/xml`, `application/atom+xml`, `application/rss+xml`). An entry of `Types` is a media type, all subtypes of a type (`text/*`) or a structured syntax suffix (`+xml` takes `application/atom+xml` and `image/svg+xml`); an invalid entry panics. `Cacheable` replaces the whole decision, and `CacheableTypes(types...)` is the building block for one of your own. `CacheControl` gives those responses a `Cache-Control` when they have none. Flushed responses are streamed as they are. Put `Compress` outside `ETag`: the weak `ETag` stays the same whether the page goes out compressed or not.

```go
r.Use(middleware.ETag(middleware.ETagOptions{
    Types: []string{"text/html", "+xml", "application/json"},
}))
```

`ETag` holds a response in a buffer from a `sync.Pool` until it is complete. A buffer that grew beyond 256 KiB for a large response is left to the garbage collector instead of going back to the pool, so one large page does not keep its memory for good.

`WeakETag(body)` returns the `ETag` the middleware gives a body, and `ETagMatches(ifNoneMatch, etag)` reports whether an `If-None-Match` header names it, for handlers that tag their responses themselves.

```go
r.Use(middleware.Compress())
r.Use(middleware.ETag(middleware.ETagOptions{
    CacheControl: func(req *http.Request) string { return "no-cache" },
}))
```

### Last-Modified

`LastModified` answers `If-Modified-Since` with `304 Not Modified`, without the body, when the handler's response has status 200 and a `Last-Modified` that is not newer than the date the client sent, compared to the second. It follows RFC 9110: only `GET` and `HEAD` are answered, a request with `If-None-Match` passes as it is (the entity tag decides, see `ETag`), and an `If-Modified-Since` that is not a valid HTTP date is ignored. A request without `If-Modified-Since` costs nothing.

The handler sets `Last-Modified` and renders the response, which is then dropped:

```go
r.Use(middleware.Compress())
r.Use(middleware.ETag())
r.Use(middleware.LastModified)

r.Get("/feed.atom", func(w http.ResponseWriter, req *http.Request) {
    w.Header().Set("Last-Modified", feed.Updated().UTC().Format(http.TimeFormat))
    // render the feed
})
```

Placement:

- Put `LastModified` inside `ETag`, as above: a request answered by its date then costs no hash. That 304 carries no `ETag`; clients keep the one they have. Outside `ETag`, the 304 carries the `ETag`, but the body is hashed first.
- Behind the output cache, the cache answers both conditions itself, from its entries (see [Output cache](#output-cache)), and passes neither on to the handler: `LastModified` then only sees requests the cache skips, such as those of signed-in visitors.

A date can only tell that nothing changed when every change makes it newer. When a response can change without its `Last-Modified` moving forward, such as a feed whose newest entry is taken off the site, a client that only sends `If-Modified-Since` is told its old copy is current. Give such a response a `Last-Modified` that includes those changes (such as the time of the content's last change), or none. Browsers send `If-None-Match` as well, which decides first.

### Output cache

The output cache of `NewCache` keeps complete responses to `GET` requests in memory and serves them again, to `GET` and `HEAD`, without calling the handler: for pages that every visitor sees alike, rendered per request. The application says what may be cached and when an entry is stale; the cache knows nothing about pages:

```go
cache := middleware.NewCache(middleware.CacheOptions{
    Version: func(req *http.Request) uint64 { return content.Version() }, // raised on every change
    Skip:    func(req *http.Request) bool { return signedIn(req) },
    Key: func(req *http.Request) (string, bool) {
        if req.URL.Query().Has("q") {
            return "", false // search results are not cached
        }
        return middleware.CacheKey(req)
    },
})

r.UseAll(cache.Middleware, middleware.Compress(), middleware.ETag(middleware.ETagOptions{
    CacheControl: func(req *http.Request) string { return "no-cache" },
}), middleware.LastModified)

stats := cache.Stats() // Hits, Misses, Skips, Evictions, Entries, Bytes
```

- **What is stored:** the status, the body and the headers the response set inside the cache. Headers set outside it before it runs, such as a request ID, stay those of each request. A hit sets `Content-Length`.
- **What is not:** only status 200 is stored, and 404 with `NotFound`. Responses that set a cookie, say `private` or `no-store` in `Cache-Control`, say `Vary: *`, are flushed (streamed), or have a body over `MaxEntryBytes` (default 1 MiB) go out as they are and are not stored. Requests with `Authorization`, `Range` or `Upgrade` headers, and those `Skip` reports or `Key` refuses, pass by the cache and count as skips; other methods pass by uncounted. A `HEAD` request with nothing stored is answered by the handler and stores nothing.
- **Keys and Vary:** `Key` defaults to `CacheKey`: the scheme, host, path and query the request arrived with (`Scheme`, `Host` and the raw request URI, so a prefix a mount stripped still counts). Within a key, the responses are told apart by the values of the request headers their `Vary` names, such as `HX-Request`: each combination is an entry of its own. `Accept-Encoding` counts only as whether gzip is accepted, so all browsers share one compressed entry. `VaryValue` narrows a header further, such as the cookies that change a page out of `Cookie`. The responses of one key may name different `Vary` lists, such as a whole page (`Vary: HX-Request`) and an HTMX navigation of the same address (`Vary: HX-Request, HX-Current-URL`): the entries of up to `MaxVarySets` lists (default 4) are kept side by side, and a request is answered by the list stored most recently that has an entry for it. A further list evicts the entries of the list stored least recently.
- **Staleness:** an entry is used for `TTL` (default 60 seconds; negative: until its version changes) and while `Version` returns the version it was stored at. A stale entry is rendered again by the next request and replaced. Several processes do not see each other's versions: the TTL bounds how long one serves an older page.
- **Memory:** entries are evicted least recently used first beyond `MaxBytes` (default 32 MiB), counting body, key, headers and a fixed overhead, and beyond `MaxEntries` when set. `Evictions` counts these and the entries of `Vary` lists beyond `MaxVarySets`.
- **Bursts:** requests for a missing or stale entry wait for the one request that renders it and are served from it, so a burst after a change renders once. A request whose variant turns out to differ waits for one render of its own variant in turn, so a burst of whole pages and navigations of a new address renders each once; a request whose response could not be stored renders its own.
- **304:** conditional `GET` and `HEAD` requests are answered `304 Not Modified` by the cache, for a stored 200, following RFC 9110: a request with `If-None-Match` when it names the stored `ETag`, and otherwise a request with `If-Modified-Since` when the stored `Last-Modified` is not newer, to the second. Only a fresh entry answers: an entry stale by `Version` or `TTL` is rendered again first, so a change that raises the version is never hidden behind the old entry's date. The request that renders an entry is answered 304 as well: the cache passes neither `If-None-Match` nor `If-Modified-Since` on to the handler, stores the whole response and answers the conditions itself.

Place it outside `Compress` and `ETag`, as above:

- With `Compress` inside, the compressed body is what is stored, so a hit costs no compression. Clients that do not accept gzip get an entry of their own, rendered once. With `Compress` outside, every hit is compressed again.
- With `ETag` inside, the `ETag` is stored with the entry and a hit costs no hash. With `ETag` outside, every hit is hashed again, and the cache cannot answer `If-None-Match` itself.
- `LastModified` goes inside, next to the handler: it answers the requests the cache skips. Outside the cache it would answer `If-Modified-Since` before the cache sees whether the entry is stale.
- Middleware that must count or see every request, such as page statistics or access logs, goes outside the cache.

`StatusHeader: "X-Cache"` adds `X-Cache: hit`, `miss` or `skip` to responses, for development. A hit costs about 0.6 µs and one allocation (the default key); a miss costs what the handler costs, plus copying the response.

### Rate limiting

`RateLimit` allows each client at most `Limit` requests per sliding `Window`, counted in memory per process, and answers others with `429 Too Many Requests` or `OnLimit`. `Key` decides who is counted (default `ClientIP`), and `Skip` leaves requests out, such as safe methods:

```go
r.Post("/comments", post).Use(middleware.RateLimit(middleware.RateLimitOptions{
    Limit:   5,
    Window:  time.Minute,
    Skip:    middleware.SafeMethod,
    OnLimit: http.HandlerFunc(throttled),
}))
```

`NewLimiter(limit, window)` is the counter underneath, for limits a middleware does not fit: `limiter.Allow(key, time.Now())` counts an event and reports whether it is within the limit. A zero limit or window turns limiting off.

### Body limits

`BodyLimit(n)` limits request bodies to `n` bytes as `http.MaxBytesHandler` does: reading more fails with an `*http.MaxBytesError`. With `OnTooLarge`, requests whose `Content-Length` is over the limit are answered before the handler runs:

```go
r.Use(middleware.BodyLimit(1 << 20))
r.Post("/upload", upload).Use(middleware.BodyLimit(64<<20, middleware.BodyLimitOptions{OnTooLarge: http.HandlerFunc(tooLarge)}))
```

### Cache and other headers

`NoStore` sets `Cache-Control: no-store`, `CacheControl(value)` another `Cache-Control`, and `Headers` any headers, before the handler runs, so the handler can still change them. `DefaultImmutable` is a `Cache-Control` for versioned URLs, whose content changes with their version: `public, max-age=31536000, immutable`, kept for a year without asking again. `SecurityHeaders` sets `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Content-Security-Policy` and `X-Robots-Tag`; without options the first three (`nosniff`, `DENY`, `no-referrer`), with options only the ones given, so it also sets a single header for a few routes. Its `ContentSecurityPolicy` is merged into the policy of the response directive by directive, as `CSP` does (see [Content-Security-Policy](#content-security-policy)); the other headers replace earlier values:

```go
r.Group("/admin", func(admin *router.Router) {
    admin.Use(middleware.NoStore)
    admin.Use(middleware.SecurityHeaders(middleware.SecurityHeadersOptions{
        ContentTypeOptions: "nosniff",
        FrameOptions:       "DENY",
        XRobotsTag:         "noindex, nofollow",
    }))
})

// Pages with a token in their URL, such as a password reset link.
r.With(func(reset *router.Router) {
    reset.Use(middleware.SecurityHeaders(middleware.SecurityHeadersOptions{ReferrerPolicy: "no-referrer"}))
    reset.Get("/password/reset/{token}", resetForm)
    reset.Post("/password/reset/{token}", resetPassword)
})

// Responses only the visitor may keep, kept out of search engines.
r.Get("/registrations/{code}", registration).Use(
    middleware.CacheControl("private, no-store"),
    middleware.SecurityHeaders(middleware.SecurityHeadersOptions{XRobotsTag: "noindex"}),
)
```

### Content-Security-Policy

`CSP(policy)` merges a policy into the `Content-Security-Policy` of every response, directive by directive, and lets handlers add sources, such as the hash of an inline script, until the response is written. `StrictCSP` allows only what the site serves itself: no inline scripts or styles, no `eval`, no plugins, forms, `<base>` and framing limited to the site:

```text
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'
```

```go
r.Use(middleware.CSP(middleware.StrictCSP))

r.Group("/admin", func(admin *router.Router) {
    // Replaces frame-ancestors only; the rest of the site's policy stays.
    admin.Use(middleware.CSP("frame-ancestors 'none'"))
})

func page(w http.ResponseWriter, r *http.Request) {
    // Allows the import map, an inline script, by its hash.
    middleware.AddCSPSource(w, "script-src", importMap.ScriptHash())
    render(w, r)
}
```

The policy of a response is made in this order. A later step wins over an earlier one for the directives it names and keeps the others:

1. The policy the response has when `CSP` runs, set by an outer middleware: another `CSP`, `SecurityHeaders` or `Headers`.
2. The policy of `CSP`: its directives replace those of the same name. `CSP("frame-ancestors 'none'")` changes only `frame-ancestors`.
3. What the handler, or a middleware inside `CSP`, leaves in the header when the response is written: its directives replace those of the same name. A handler that sets `frame-ancestors https://parent.example` changes only `frame-ancestors`; a handler that deletes the header sends no policy.
4. The sources of `AddCSPSource`, added to the directives as they then stand.

So the innermost middleware and the handler have the last word: put the site-wide policy outside, and the policies of groups of routes inside. `CSP` sets the merged policy of steps 1 and 2 before the handler runs, so the handler can read it, and merges steps 3 and 4 when the status is written, or after the handler returns without writing. Only the first policy of the header is merged; further ones (several header values, or one value with policies separated by `,`), which browsers enforce as well, stay as they are.

`AddCSPSource(w, directive, sources...)` only ever allows more. A directive the policy lacks starts from the one browsers fall back to, so adding a hash to `script-src` of `default-src 'self'` gives `script-src 'self' 'sha256-…'`, not a `script-src` with the hash alone; `'none'` makes way for added sources. It changes the header at once and, under `CSP`, again when the response is written, so the sources also survive a handler that sets the header later. After the response is written it changes nothing, and a response without a policy stays without one. Browsers ignore `'unsafe-inline'` in a directive with a hash or a nonce, and use `script-src-elem` rather than `script-src` for `<script>` elements when the policy has it.

Prefer hashes to nonces for pages an output cache keeps: a hash is the same for every response with the same content, while a nonce must be new for each response, and a cached page carries the nonce of the response that was stored. Put `CSP` inside the cache, so the policy with the page's hashes is stored and served with the page:

```go
r.Use(cache.Middleware)
r.Use(middleware.CSP(middleware.StrictCSP))
r.Use(middleware.Compress())
```

`CSPPolicy` builds and changes policies outside a request. `ParseCSP` parses one policy (names in lower case, the first of duplicate directives, duplicate sources removed) and `String` writes it back in order. `Set` replaces a directive's sources, `Add` adds sources as `AddCSPSource` does, `Delete` removes a directive and `Override` sets every directive of another policy. `Sources` returns a directive's sources, `IsZero` reports a policy without directives, and `Clone` copies a policy before a change, since a `CSPPolicy` is not safe for concurrent changes. `SplitCSP` splits header values into their policies.

```go
policy := middleware.ParseCSP(middleware.StrictCSP)
policy.Add("style-src", "'sha256-…'")
policy.Set("frame-ancestors", "'none'")
w.Header().Set("Content-Security-Policy", policy.String())
```

### CORS

`CORS` enforces configured preflight methods and headers:

```go
r.Use(middleware.CORS(middleware.CORSOptions{
    AllowedOrigins: []string{"https://example.com"},
    AllowedMethods: []string{"GET", "POST", "OPTIONS"},
    AllowedHeaders: []string{"Content-Type", "Authorization"},
}))
```

`*` allows all origins. Entries like `*.example.com` allow subdomains but not the bare apex host. With `AllowCredentials`, the response says `Access-Control-Allow-Credentials: true`, and an all-origin policy echoes a valid request origin instead of `*`. Preflight requests are checked against the configured methods and headers before `204 No Content`. Origins that are not allowed, and preflights with methods or headers outside the configured ones, get `403 Forbidden`. `ExposedHeaders` sets `Access-Control-Expose-Headers`, and `MaxAge` the `Access-Control-Max-Age` of preflights, in seconds.

Every response says `Vary: Origin`, also one to a request without an `Origin` header, so a shared cache keeps that answer, without CORS headers, apart from the answers to the allowed origins. A preflight (`OPTIONS`) also varies by `Access-Control-Request-Method` and `Access-Control-Request-Headers`.

### Streaming

The router's and the bundled middleware's response writers support `http.Flusher`, `http.Hijacker` (except `ContentLengthMiddleware`, and `Compress` once it compresses) and `Unwrap`, so both type assertions and `http.NewResponseController` reach the connection. Server-sent events and websockets work through them, including with `HandleStatus` handlers registered.

`ContentLengthMiddleware` buffers ordinary responses to compute their length. Calling `http.NewResponseController(w).Flush()` or `http.Flusher.Flush()` switches to streaming and skips automatic `Content-Length`. Place handlers that hijack the connection, such as websockets, outside this middleware.

`Timeout` uses `http.TimeoutHandler`, which buffers the response and cannot flush. Keep streaming routes out of it.

## JSON Helpers

For small JSON APIs:

```go
type CreateUser struct {
    Name string `json:"name"`
}

func createUser(w http.ResponseWriter, req *http.Request) {
    var input CreateUser
    if err := router.DecodeJSON(req, &input); err != nil {
        _ = router.BadRequest(w, err)
        return
    }

    _ = router.JSON(w, http.StatusCreated, input)
}
```

Helpers:

- `DecodeJSON`
- `JSON`
- `Error`
- `BadRequest`
- `InternalServerError`
- `MessageError`

## Static Files

Serve a directory:

```go
r.ServeFiles("/static/", http.Dir("./static"))
```

Serve one file:

```go
r.ServeFile("/favicon.ico", "./static/favicon.ico")
```

Both return the route, so it can be named and get route middleware. The pattern of `ServeFiles` ends in a `{file...}` wildcard, or in a rest wildcard of your own, which reverses to a file's path:

```go
r.ServeFiles("/assets/", http.FS(assets)).As("assets").Use(cacheForever)
r.ServeFiles("/static/{path...}", http.Dir("./static")).As("static")
```

```html
<link rel="stylesheet" href="{{ route "assets" "file" "css/site.css" }}"> <!-- /assets/css/site.css -->
```

`/assets`, `/assets/` and `/assets/{file...}` register the same route, `GET /assets/{file...}`, which matches what `GET /assets/` matched. The file server gets the wildcard's value as its path, so files are served the same in groups, hosts and mounted routers.

Static files work inside groups and host routers:

```go
r.Host("cdn.example.com", func(cdn *router.Router) {
    cdn.ServeFiles("/assets", http.Dir("./assets"))
})
```

## Not Found and Method Not Allowed

`NotFound` and `MethodNotAllowed` answer the requests no route matches: `NotFound` when no route has the path, `MethodNotAllowed` when routes have the path, but not for the request's method. Without them the router answers as the `ServeMux` does. They, and the options `WithNotFound` and `WithMethodNotAllowed`, take an `http.Handler`; `HandleStatus` takes an `http.HandlerFunc`.

The router sets the `Allow` header before `MethodNotAllowed` runs, so the handler can read the methods. It keeps the header when the handler answers `405`, and removes it when the handler answers with another status, unless the handler changed it. A site that shows its not found page for every unmatched request uses the same handler for both, without an `Allow` header on the 404:

```go
r := router.NewSite(router.WithNotFound(http.HandlerFunc(notFound)), router.WithMethodNotAllowed(http.HandlerFunc(notFound)))
```

```go
r := router.NewSite(router.WithNotFound(http.HandlerFunc(siteNotFound)))
r.MethodNotAllowed(http.HandlerFunc(methodNotAllowed))

r.Group("/api", func(api *router.Router) {
    api.Use(jsonContentType)
    api.NotFound(jsonNotFound) // unmatched requests below /api
})
```

Unlike `HandleStatus` they only answer unmatched requests: a route that answers 404 itself, such as a lookup of a code that does not exist, keeps its own response, and their own responses are not replaced by `HandleStatus` handlers. They are not routes: they are not in `Walk`, they do not keep `Use` from being called, and every route takes precedence over them. They run inside `UseAll` middleware and inside the `Use` middleware of the router or group they are set on, also middleware added after them. The most specific one answers: a group's for paths below the group, a host router's on its host.

A mounted router answers its unmatched requests with its own handlers, or else with those of the routers it is mounted on, which see the request URL as the outer router received it. Those run without the `Use` middleware of their router: the request passed it already through the mount. A router that serves `/` itself, such as with `r.Handle("/", site)` or a router mounted at `/`, leaves its unmatched requests to that handler. The router answers unmatched requests through a `ServeMux` pattern of its own, so matched requests cost nothing extra.

## Matching Routes

`Match` returns the route a method and path would be served by, as `Walk` lists it, with routes of mounted routers below their mount path:

```go
route, ok := r.Match(http.MethodGet, "/admin/users/7")
// route.Name "admin.users.show", route.Pattern "/admin/users/{id}"
```

A path below a mounted router that none of its routes matches is the mount's: `Match` returns the mount, as `Walk` lists it, with `RouteInfo.Mount` set and the mount's labels. Everything below a mount counts as the mount's, also the paths the mounted router does not know:

```go
r.Mount("/admin", admin).Label("admin")

route, _ := r.Match(http.MethodGet, "/admin/nothing")
// route.Pattern "/admin/", route.Mount true, route.HasLabel("admin") true
```

`Owns` is `Match` for any method: the most specific route a path has, or the mount. It tells which part of an application a path belongs to, such as whether a page of a site may take it:

```go
r.Mount("/admin", admin).Label("admin")
r.Post("/login", login).Label("auth")
r.Handle("/", site).Label("site")

r.Owns("/admin/anything") // the mount /admin/, labelled admin
r.Owns("/login")          // POST /login, not the site that GET reaches
r.Owns("/about")          // the site, "/"
```

Of two routes the more specific one has a literal segment where the other has a wildcard, then more segments, then does not match the paths below it. `Owns` tries the methods the routes have one by one, so it costs about a `Match` per method; for a router whose routes are all `GET` or take any method, one.

A path the router redirects is matched where the redirect leads, with `RouteInfo.Redirect` set to that path. That covers the redirects of the `ServeMux`, of a path that is not clean to the clean one and of a path to the route with a trailing slash, also in mounted routers, and those of `RedirectTrailingSlash`. Where a route has no redirect, `Redirect` is empty:

```go
r.Get("/theme/{file...}", theme).Label("asset")
r.Mount("/admin", admin).Label("admin")

r.Match(http.MethodGet, "/theme")         // GET /theme/{file...}, Redirect "/theme/"
r.Match(http.MethodGet, "/admin//users")  // the admin's route or mount, Redirect "/admin/users"
r.Match(http.MethodGet, "/theme/site.css") // GET /theme/{file...}, Redirect ""
```

So one `Match` tells which part of an application any path belongs to, without cleaning the path first. A path whose redirects never end at a route, such as one the router and its `ServeMux` send back and forth, matches nothing.

`MatchedRoute` returns the route that served a request, as the router the request entered has it, for `UseAll` middleware such as metrics and access logs. The router records it when `TrackRoutes` is set, which costs a few allocations per request:

```go
r.TrackRoutes()
r.UseAll(middleware.Observe(func(req *http.Request, info middleware.ResponseInfo) {
    class := "unmatched"
    if route, ok := router.MatchedRoute(req); ok && len(route.Labels) > 0 {
        class = route.Labels[0]
    }

    requests.WithLabelValues(class, strconv.Itoa(info.Status)).Inc()
}))
```

A request below a mounted router that none of its routes answered, such as its 404 and 405 answers or an answer of its `UseAll` middleware, reports the mount, with `RouteInfo.Mount` set and the mount's labels, so the 404s of an admin count as the admin's. `MatchedRoute` reports false for the requests the router answers without a route or mount: its own 404 and 405 answers, `NotFound` and `MethodNotAllowed` handlers and the redirects of its trailing slash policy and its `ServeMux`; `Match` on the request's path tells where such a redirect leads. A mounted router is not a route of its own; its routes are. Any other mounted handler is, with `Mount` set as well.

When only the labels matter, `MatchedLabel`, `MatchLabel` and `OwnsLabel` give one without copying the route: the first of the route's labels that is one of those asked for, or its first label when none are asked for, and `""` when it has none of them. Their second result is what `MatchedRoute`, `Match` and `Owns` report:

```go
r.UseAll(middleware.Observe(func(req *http.Request, info middleware.ResponseInfo) {
    class, ok := router.MatchedLabel(req, "admin", "asset", "media")
    if !ok {
        class, ok = r.MatchLabel(http.MethodGet, req.URL.Path, "admin", "asset", "media") // a redirect
    }

    if class == "" {
        class = "public"
    }
    // ...
}))
```

`TrackRoutes` costs two allocations per request for the request's state; requests that enter a mounted router copy their URL once more. `MatchLabel` and `OwnsLabel` allocate little beyond what the `ServeMux` does to look a route up; `Match` and `Owns` copy the route's labels and middleware as well.

Labels tell routes apart by kind. `Label` on a route adds them to `RouteInfo.Labels`, and labels of a mount are given to every route of the mounted router:

```go
r.ServeFiles("/assets/", assets).As("assets").Label("asset")
r.Mount("/media", media).Label("media")

route, _ := r.Match(http.MethodGet, "/media/v/photo.jpg")
route.HasLabel("media") // true
```

## Custom Status Handlers

Register custom handlers for status codes emitted by the underlying router:

```go
r.HandleStatus(http.StatusNotFound, notFound)
r.HandleStatus(http.StatusMethodNotAllowed, methodNotAllowed)
```

Custom status handling uses an intercepting response writer. When a handler writes a status that has a custom handler, its response is dropped and the custom handler answers instead, without inheriting the dropped response's `Content-Type`. Once a response has started (a body was written or flushed), later statuses are not intercepted, and flushes of a dropped response are ignored.

A handler that renders its own error page keeps its response with `router.NoIntercept(w)`, before it writes the status:

```go
func page(w http.ResponseWriter, req *http.Request) {
    if missing {
        router.NoIntercept(w)
        w.WriteHeader(http.StatusNotFound)
        renderNotFound(w, req)
        return
    }
    // ...
}
```

It applies to the status handlers of the router and of the routers it is mounted on. It reaches the router through response writers that implement `Unwrap() http.ResponseWriter`, as the bundled middleware and `http.ResponseController`-aware wrappers do. Behind a wrapper without `Unwrap`, set the `router.HeaderFlagDoNotIntercept` header instead; the router removes it before the response is sent.

Status handlers get the request as the router received it: route middleware has not run for it, `UseAll` middleware has (see [Middleware for every request](#middleware-for-every-request)).

For pages of your own for requests no route matches, use `NotFound` and `MethodNotAllowed` (see [Not Found and Method Not Allowed](#not-found-and-method-not-allowed)), which leave the 404s of routes alone.

The no-custom-status path is optimized and allocation-free in the router benchmark.

## Trailing Slashes

Trailing slash redirects are off by default:

```go
r.RedirectTrailingSlash(true)
```

With them on, a path that ends in a slash, other than `/`, redirects to the path without it, with `router.DefaultRedirectStatusCode` (`307 Temporary Redirect`). A path that a route serves with its slash keeps it: a route such as `/docs/`, `/admin/{$}` or `/files/{name...}`, the root of `ServeFiles`, and such routes of a mounted router.

Groups and hosts can set their own trailing slash policy:

```go
r.Group("/admin", func(admin *router.Router) {
    admin.RedirectTrailingSlash(true)
})
```

## Route Walking and Diagnostics

Use `Walk` to inspect registered routes. `RouteInfo.Path` is the path for reverse routing and `RouteInfo.Pattern` the registered `ServeMux` pattern, such as `/admin/` and `/admin/{$}`:

```go
err := r.Walk(func(route router.RouteInfo) error {
    log.Printf("%s %s %s %s", route.Host, route.Method, route.Path, route.Name)
    log.Printf("handler: %T middleware: %d", route.Handler, len(route.Middlewares))
    return nil
})
```

Or print a route table:

```go
fmt.Print(r.RouteTable())
```

`WriteRouteTable(w)` writes the same table to an `io.Writer`.

Duplicate route names and duplicate host/method/pattern registrations fail during registration.

## OpenAPI

Enable OpenAPI docs:

```go
r.UseOpenapiDocs(true)
```

Call it before the routes and groups it is meant for: docs are recorded when a route is registered, and groups take the setting when they are made. `router.DefaultUseOpenapiDocs` is the setting routers made by `New` start with, `false` unless you change it.

Attach docs to routes:

```go
r.Get("/users/{id}", showUser, router.Docs{
    Tags:    []string{"users"},
    Summary: "Show user",
    Out: map[string]router.DocOut{
        "200": {
            ApplicationType: "application/json",
            Description:     "OK",
            Object:          User{},
        },
    },
})
```

Access the document:

```go
doc := r.OpenAPI()
```

Add the servers the API is reached at:

```go
r.AddServerEndpoint("https://api.example.com", "Production")
```

Supported methods:

- `GET`
- `HEAD`
- `POST`
- `PUT`
- `PATCH`
- `DELETE`
- `OPTIONS`

Schema generation supports:

- JSON field names and ignored fields
- nested structs
- embedded structs
- slices and arrays
- maps via `additionalProperties`
- pointers as nullable
- `time.Time` as `date-time`
- `format` tags
- `enum` tags such as `enum:"admin|user"`
- required fields from `validate:"required"`, `binding:"required"`, or `required:"true"`

OpenAPI support is useful, but still intentionally lightweight. For highly detailed API specs, you may still want explicit docs or schema overrides in your application.

## Production Notes

The router is intended to be configured during startup. After the first request is served, registration freezes, also on routers mounted on it, and later mutation panics or returns an error through the `Try` APIs.

Recommended production defaults:

```go
r := router.NewSite()
r.UseAll(
    middleware.RequestID,
    middleware.RealIPWithOptions(middleware.RealIPOptions{
        TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
    }),
    middleware.Logger(),
    middleware.Recover,
    // On every response, also the router's own redirects and plain 404s.
    middleware.SecurityHeaders(middleware.SecurityHeadersOptions{
        ContentTypeOptions: "nosniff",
        ReferrerPolicy:     "strict-origin-when-cross-origin",
    }),
)
r.Use(middleware.SecurityHeaders(middleware.SecurityHeadersOptions{FrameOptions: "DENY"})) // pages
r.Use(middleware.Timeout(10 * time.Second)) // not around streaming routes
// routes ...

log.Fatal(http.ListenAndServe(":8080", r))
```

Before shipping an app:

- Register routes only during startup.
- Prefer `Try*` registration if you want explicit bootstrap errors.
- Use `RealIPWithOptions` behind proxies, listing only your own proxies.
- Register middleware before routes, and use `UseAll` (or wrap the router) for middleware that must see every request.
- Set headers every response must carry, such as `X-Content-Type-Options`, with `UseAll`: `Use` middleware does not run for the router's own answers, its clean-path and trailing-slash redirects and plain 404 and 405 responses, since no route answered them. Headers that belong to pages, such as framing, can stay on `Use`.
- Create `Compress` with one or two levels: each level in use keeps up to `min(GOMAXPROCS, 16)` idle gzip writers of about 1 MB.
- Explicitly flush streaming responses and use middleware that supports flushing.
- Run `go test ./...`, `go test -race ./...`, and `go vet ./...`.
- Add app-level tests for mounted routers, subdomains, and custom middleware.

See [PRODUCTION.md](PRODUCTION.md) for the current hardening checklist.

## Upgrading

### From v0.11.0

These changes break compilation:

- `TryHandle`, `TryHandleFunc` and `TryMount` return `(*RouteRegistration, error)`; they returned `error`. Write `_, err := r.TryHandle(...)`. `TryMount` and `Mount` also take `...MountOption`.
- `Handle`, `HandleFunc`, `Mount`, `ServeFiles`, `ServeFile` and `RouteRegistration.As` return `*RouteRegistration`; they returned nothing. Plain calls compile as before. Method values of the old type, such as `var f func(string, http.Handler) = r.Handle`, and interfaces the router satisfied with the old signatures do not.
- `middleware.LoggerOptions.Logger` is a `*slog.Logger`; it was a `*log.Logger`.
- `RouteInfo`, `middleware.SecurityHeadersOptions` and `middleware.LoggerOptions` have new fields, so struct literals without field names no longer compile.
- `middleware.LoggerOptions` has a func field, `Attrs`, so it is no longer comparable: comparing it with `==` or using it as a map key no longer compiles.

These change behaviour.

Security:

- `CORS` sends `Vary: Origin` on every response, also to a request without an `Origin`; it sent it only when the request had one, so a shared cache could answer an allowed origin with a stored response that has no CORS headers. Nothing to change.
- **`SecurityHeaders` merges its `ContentSecurityPolicy` directive by directive:** its directives replace only those of the same name in a policy set earlier, and a handler that sets the header changes only the directives it names. Before, it set the header before the handler ran, replacing an earlier policy, and a handler that set the header replaced it in turn. The policy is written out again, so alone it sends an equivalent header rather than the same text; to send a policy exactly as written, leave `ContentSecurityPolicy` empty and set the header yourself.
- **`RealIP` no longer trusts forwarding headers:** the client IP is the address of the direct peer. Before, it took `X-Forwarded-For` (the leftmost entry), `X-Real-IP` or `Forwarded` from any client, so anyone could choose the IP that logs and rate limits saw. Behind proxies, use `RealIPWithOptions(RealIPOptions{TrustedProxies: ...})` (see [Client IP](#client-ip)).
- `RealIPWithOptions` reads `X-Forwarded-For` from the right, skipping trusted proxies, and accepts only IP addresses in the headers; it took the leftmost entry, whatever it was. An invalid `TrustedProxies` entry panics, where it was ignored; the new `TryRealIP` returns the error instead.
- Both leave `r.RemoteAddr` as it is unless the client differs from the peer, and then set it to `ip:0` on the request they pass on. Before, they always set a bare IP without a port, on the request itself, so middleware outside them saw it as well. Inside them, read the client IP with the new `middleware.ClientIP(r)`.
- **`RequestID` replaces invalid incoming IDs.** An `X-Request-ID` is kept only when it is 1 to 64 letters, digits and `-_.:`, so a client can no longer put arbitrary text into your logs; any non-empty value was kept. The new `middleware.ValidRequestID` is the rule.

Logging and recovery:

- **`Logger` logs through slog.** It logs one record "request" at Info with `method`, `uri`, `status`, `bytes`, `duration`, `client_ip`, `request_id` (when there is one) and, for a handler that panicked, `panicked`, to `slog.Default()` unless `LoggerOptions.Logger` is set; it wrote a `[go-router] ...` line with the `log` package. Requests whose handler panicked are logged too (status 500 when none was written), and `w.(http.Flusher)` and `w.(http.Hijacker)` work through it.
- `Logger` passes every `WriteHeader` call on; it dropped the calls after the first. So the final status after a `103 Early Hints` now goes out, and net/http logs a second `WriteHeader` of a handler as superfluous.
- **`Recover` logs through slog, with the stack, and aborts started responses.** It logs "panic recovered" at Error with `panic`, `method`, `path`, `request_id` (when there is one) and `stack`; it logged the panic value alone with the `log` package. A response that has already started is aborted with `http.ErrAbortHandler`, where a 500 was written into it, and an `http.ErrAbortHandler` panic passes through unlogged.
- `Recover` passes a wrapped `ResponseWriter` on, which hides `io.ReaderFrom`; it passed the writer on as it was. Files served below it are therefore copied through a buffer instead of sent with sendfile: slower for large files, not wrong.
- `Timer` logs "request timed" through slog with `duration`, `method` and `path`; it used the `log` package.

Routing:

- `Use` after routes panics ("router: Use after routes were registered"), since the middleware would miss the routes before it; it was added silently. Call `Use` before the routes, or put the routes that need other middleware in a `Group` or `With`.
- Reverse routing keeps a trailing slash: the path of `/users/` is `/users/` and that of `/admin/{$}` is `/admin/`, where both lost it. This holds for `RoutePath`, `RoutePathWithParams`, `RouteURL`, `RouteInfo.Path` in `Walk` and `Routes`, and the template functions, also for the root route of a mounted router (`/admin/`, was `/admin`).
- `/users` and `/users/` (or `/admin/` and `/admin/{$}`) are two routes; registering both panicked as a duplicate route.
- With `RedirectTrailingSlash(true)`, a path that a route serves with its trailing slash is served, such as `/docs/`, `/admin/{$}` or the root of `ServeFiles`. It was redirected to the path without the slash, which the `ServeMux` redirected back, again and again.
- `RoutePathWithParams` and `RouteURL` fill `{name...}` wildcards, segment by segment; they left them as they were. An unknown route or a missing parameter still gives `""` or the placeholder, and now logs a warning, to `slog.Default()`. The new `RoutePathArgs` returns errors instead.
- `ServeFiles` registers `GET <prefix>/{file...}`; it registered `GET <prefix>/`. `Walk` and `RouteTable` show `/assets/{file...}` where they showed `/assets`.
- The `FuncMap` functions `isActiveRoute` and `isActiveRouteExact` are `func(any, string, ...any) (bool, error)`, and `isActiveRouteContains` is `func(any, string) (bool, error)`; they were `func(*url.URL, string) bool`. Templates call them as before, but Go code that type-asserts them to the old types fails.
- `isActiveRoute` compares whole path segments, and matches routes with parameters: `/admin/news` is no longer active on `/admin/newsletter`, and `/news/{id}` is active on `/news/7`. `isActiveRouteExact` follows the trailing slash of the route: `/admin/` is active on `/admin/`, not on `/admin`. Invalid arguments fail the template.
- `FuncMap` has three more functions: `route`, `hasRoute` and `routeOr`. Where you merge it into one `template.FuncMap` with functions of your own of those names, the map merged last wins; merge yours after it to keep them.

Mounted routers:

- Reverse routing on a mounted router (`child.RoutePath`, `RouteURL`, its `FuncMap`) gives the public path, such as `/admin/x`; it gave the path within the child, `/x`.
- Routes and names registered on the child after `Mount` reach the parent; only those registered before `Mount` did. A clash with a route or name of the parent therefore panics in the child's registration call, where it went unnoticed.
- A handler with a `Router() *Router` method is mounted as that router: its routes are merged into the parent, and the handler sees the full URL. It was a plain handler behind `http.StripPrefix`.
- Mounting a router on itself, or on a router mounted on it, panics; nothing stopped it.
- A mounted router is frozen when the parent serves its first request; it was frozen when a request first reached it.
- The redirects of a mounted router, those of `RedirectTrailingSlash` and those of the `ServeMux` (such as `/users` to `/users/`), point to the public path, such as `/admin/users/`. They pointed to the path within the child, `/users/`, outside the mount.
- The middleware of `ServeFiles` and `ServeFile` routes in a mounted router sees the full request URL, as that of its other routes did; it saw the path within the child.
- OpenAPI paths of a mounted router keep their trailing slash: the child's `/users/` and `/` are `/admin/users/` and `/admin/`, where they were `/admin/users` and `/admin`.

Status handlers (`HandleStatus`):

- A `Write` to a response that a status handler replaces reports the bytes as written; it reported 0, a short write.
- `Content-Type` and `Content-Length` of the replaced response are removed before the status handler runs; its response inherited them.
- A status written after the response started passes through, and so does a 1xx status; before, a status handler for that status took over.
- `Flush` and `Hijack` work through it: with any status handler registered, `w.(http.Flusher)` and `w.(http.Hijacker)` failed.
- The `do_not_intercept` header (`HeaderFlagDoNotIntercept`) is removed also from a response that starts with `Write` or `Flush`; it leaked then.

OpenAPI:

- The automatic OPTIONS routes print nothing to standard output; there is a Debug record instead, to `slog.Default()`. A path with an OPTIONS route of its own is left alone, where the first request panicked.

## Benchmarks

The router has a direct benchmark against raw `http.ServeMux` using the same route set and a minimal response writer.

Recent local result:

```text
BenchmarkRouterVsServeMuxRouter  280271 ns/op   0 B/op   0 allocs/op
BenchmarkRouterVsServeMuxStdlib  260223 ns/op   0 B/op   0 allocs/op
```

In that benchmark each operation serves 2000 requests, so the hot path is roughly:

```text
go-router: ~140 ns/request
ServeMux:  ~130 ns/request
```

Benchmark numbers vary by machine. Run them locally with:

```bash
go test -run ^$ -bench . -benchmem
```

## Examples

- `example/simple`
- `example/openapi`
- `example/production`

## Status

This project is suitable for controlled production use in your own applications, especially websites, internal tools, SaaS-style apps, and APIs where you own the route patterns and rollout.

It is not yet as battle-tested as mature routers like `chi`. If you use it broadly, keep tests close to your app, run race tests, and harden based on real production behavior.

## License

MIT. See [LICENSE](LICENSE).
