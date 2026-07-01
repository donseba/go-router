<p align="center">
    <img src="./assets/go-router-logo.png" alt="go-doc" width="420">
</p>

`go-router` is a lightweight router for Go websites and APIs. It builds on the standard library `net/http` `ServeMux` and adds the pieces most small and medium applications usually end up needing: route groups, middleware, named routes, host and subdomain routing, mounted routers, OpenAPI helpers, route walking, static files, and practical middleware.

It is designed for applications that want to stay close to `net/http` without giving up website/API ergonomics.

## Features

- Standard-library based routing with Go's method-aware `ServeMux` patterns.
- Route groups and middleware-only groups.
- Exact host, subdomain, and wildcard subdomain routing.
- Named routes with reverse path and URL generation.
- Mounted routers with named route, route walk, and OpenAPI metadata propagation.
- Route walking and route table diagnostics.
- Static file and single-file serving.
- Custom status handlers.
- Optional trailing slash redirects.
- OpenAPI metadata and schema helpers.
- JSON request/response helpers.
- Path parameter helper functions.
- Bundled middleware for request IDs, logging, recovery, timeouts, security headers, CORS, real IP, content length, and timing.
- Registration freezes after the first request to avoid runtime mutation races.

## Install

```bash
go get github.com/donseba/go-router
```

`go-router` targets modern Go and uses the standard library routing improvements introduced in Go 1.22.

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
- `TryMount`
- `TryAs`

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

Exact hosts take precedence over wildcard hosts. The apex domain does not match a wildcard subdomain. Unknown hosts fall back to the default router.

## Named Routes and URLs

Name a route with `As`:

```go
r.Get("/users/{id}", showUser).As("users.show")
```

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

Template helpers are available through `FuncMap`:

```go
tmpl := template.New("page").Funcs(r.FuncMap())
```

Included template functions:

- `routePath`
- `routePathWithParams`
- `routeHost`
- `routeURL`
- `isActiveRoute`
- `isActiveRouteExact`
- `isActiveRouteContains`

## Mounting

Mount any `http.Handler` under a path:

```go
r.Mount("/debug", http.DefaultServeMux)
```

Mounted `*router.Router` instances also contribute named routes, walk metadata, and OpenAPI paths/components:

```go
api := router.New(http.NewServeMux(), "API", "1.0.0")
api.Get("/users/{id}", showUser).As("api.users.show")

r.Mount("/api", api)

r.RoutePathWithParams("api.users.show", map[string]any{"id": 123})
// "/api/users/123"
```

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

The bundled middleware package includes:

- `RequestID`
- `Logger`
- `Recover`
- `Timeout`
- `RealIP`
- `RealIPWithOptions`
- `SecurityHeaders`
- `CORS`
- `ContentLengthMiddleware`
- `Timer`

For production proxy deployments, prefer `RealIPWithOptions`:

```go
r.Use(middleware.RealIPWithOptions(middleware.RealIPOptions{
    TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
}))
```

`RealIP` trusts forwarding headers from any client and is mainly a convenience helper.

`CORS` enforces configured preflight methods and headers:

```go
r.Use(middleware.CORS(middleware.CORSOptions{
    AllowedOrigins: []string{"https://example.com"},
    AllowedMethods: []string{"GET", "POST", "OPTIONS"},
    AllowedHeaders: []string{"Content-Type", "Authorization"},
}))
```

Avoid `ContentLengthMiddleware` for streaming, SSE, websockets, or reverse proxy style handlers because it buffers the response.

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

Static files work inside groups and host routers:

```go
r.Host("cdn.example.com", func(cdn *router.Router) {
    cdn.ServeFiles("/assets", http.Dir("./assets"))
})
```

## Custom Status Handlers

Register custom handlers for status codes emitted by the underlying router:

```go
r.HandleStatus(http.StatusNotFound, notFound)
r.HandleStatus(http.StatusMethodNotAllowed, methodNotAllowed)
```

Custom status handling uses an intercepting response writer. The no-custom-status path is optimized and allocation-free in the router benchmark.

## Trailing Slashes

Trailing slash redirects are off by default:

```go
r.RedirectTrailingSlash(true)
```

Groups and hosts can set their own trailing slash policy:

```go
r.Group("/admin", func(admin *router.Router) {
    admin.RedirectTrailingSlash(true)
})
```

## Route Walking and Diagnostics

Use `Walk` to inspect registered routes:

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

Duplicate route names and duplicate method/path/host registrations fail during registration.

## OpenAPI

Enable OpenAPI docs:

```go
r.UseOpenapiDocs(true)
```

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

The router is intended to be configured during startup. After the first request is served, registration freezes and later mutation panics or returns an error through the `Try` APIs.

Recommended production defaults:

```go
r.Use(middleware.RequestID)
r.Use(middleware.RealIPWithOptions(middleware.RealIPOptions{
    TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
}))
r.Use(middleware.Logger())
r.Use(middleware.Recover)
r.Use(middleware.Timeout(10 * time.Second))
r.Use(middleware.SecurityHeaders())
```

Before shipping an app:

- Register routes only during startup.
- Prefer `Try*` registration if you want explicit bootstrap errors.
- Use `RealIPWithOptions` behind proxies.
- Avoid response buffering middleware for streaming.
- Run `go test ./...`, `go test -race ./...`, and `go vet ./...`.
- Add app-level tests for mounted routers, subdomains, and custom middleware.

See [PRODUCTION.md](PRODUCTION.md) for the current hardening checklist.

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
