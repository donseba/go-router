# go-router Documentation
go-router is a lightweight, flexible, and idiomatic HTTP router for Go web applications. It leverages Go's standard `net/http` package and the latest routing enhancements in Go 1.22 to provide powerful routing capabilities without external dependencies.

## Overview

- **Method-Based Routing**: Easily define routes for `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS`, and `HEAD` methods.
- **Route Grouping**: Organize routes under common base paths using groups.
- **Middleware Support**: Apply middleware functions globally or per group.
- **Custom 404, 405 and 500 Handlers**: Set custom handlers for NotFound (404) and MethodNotAllowed (405) responses.
- **Trailing Slash Handling**: Configure automatic redirection of trailing slashes.
- **Static File Serving**: Serve static files and directories seamlessly.
- **Built on Standard Library**: Utilizes Go's net/http package, ensuring performance and reliability.
- **No External Dependencies**: Keeps your application lightweight and maintainable.

## Installation

Ensure you have Go 1.22 or later installed to leverage the latest routing enhancements.

To install go-router, run:

```bash
go get github.com/donseba/go-router
```
---
## Package Contents

### Types

#### Router

The `Router` struct is the core of the package, providing methods to define routes, apply middleware, and configure routing behavior.
Fields

- **mux *http.ServeMux**: The underlying HTTP request multiplexer.
- **basePath string**: The base path for the router, used in route grouping.
- **redirectTrailingSlash bool**: Determines whether to redirect trailing slashes to their non-trailing counterparts.
- **middlewares []Middleware**: A slice of middleware functions applied to the router.
- **notFoundHandler http.HandlerFunc**: Custom handler for 404 Not Found responses.
- **methodNotAllowedHandler http.HandlerFunc**: Custom handler for 405 Method Not Allowed responses.

#### Middleware

`type Middleware func(http.Handler) http.Handler`

Represents a middleware function that wraps an http.Handler to perform actions before or after the handler executes.

### Functions

#### New(ht *http.ServeMux) *Router

Creates a new Router instance using the provided http.ServeMux.

#### NewDefault() *Router

Creates a new Router instance with a default http.ServeMux.

### Methods
Route Definition Methods

Define routes for specific HTTP methods.

- (*Router) Get(pattern string, handler http.HandlerFunc)
- (*Router) Head(pattern string, handler http.HandlerFunc)
- (*Router) Post(pattern string, handler http.HandlerFunc)
- (*Router) Put(pattern string, handler http.HandlerFunc)
- (*Router) Patch(pattern string, handler http.HandlerFunc)
- (*Router) Delete(pattern string, handler http.HandlerFunc)
- (*Router) Options(pattern string, handler http.HandlerFunc)v

#### Parameters

- **pattern string**: The URL pattern for the route. Patterns can include placeholders like {id}. A pattern that ends in “/” matches all paths that have it as a prefix, as always. To match the exact pattern including the trailing slash, end it with {$}, as in /exact/match/{$}.
- **handler http.HandlerFunc**: The function to handle requests matching the pattern and method.

### Grouping Routes

`(*Router) Group(basePath string, fn func(*Router))`

Organize routes under a common base path.

####  Parameters

- **basePath string**: The base path for the group.
- **fn func(*Router)**: A function that receives a sub-router for defining grouped routes.

### Middleware

`(*Router) Use(middleware Middleware)`

Apply middleware functions to the router.

#### Parameters

- **middleware Middleware**: A middleware function to be applied.

### Custom Handlers

v(*Router) NotFound(handler http.HandlerFunc)**: Set a custom handler for 404 Not Found responses.
- **(*Router) MethodNotAllowed(handler http.HandlerFunc)**: Set a custom handler for 405 Method Not Allowed responses.

#### Parameters

    handler http.HandlerFunc: The function to handle the specific response.

### Trailing Slash Handling

`(*Router) RedirectTrailingSlash(redirect bool)`

Configure automatic redirection of trailing slashes.
#### Parameters

- **redirect bool**: If true, requests with trailing slashes are redirected to their non-trailing counterparts.

### Serving Static Files

- **(*Router) ServeFiles(pattern string, fs http.FileSystem)**: Serve static files from a directory.
- **(*Router) ServeFile(pattern string, filepath string)**: Serve a single static file.

#### Parameters

- **pattern string**: The URL pattern under which the files are served.
- **fs http.FileSystem**: The file system to serve files from.
- **filepath string**: The path to the file to be served.

### HTTP Handling

`(*Router) ServeHTTP(w http.ResponseWriter, req *http.Request)`

Implements the http.Handler interface, allowing the router to serve HTTP requests.

### Internal Methods
`handle(method, pattern string, handler http.HandlerFunc)`

An internal method used to register handlers for specific HTTP methods and patterns.
#### Parameters

- **method string**: The HTTP method (e.g., GET, POST).
- **pattern string**: The URL pattern.
- **handler http.HandlerFunc**: The handler function.

--- 

## Configuration Variables

- **DefaultRedirectTrailingSlash bool**: The default setting for trailing slash redirection (default is true).

--- 
## Usage Guidelines

### Defining Routes

Use the provided methods to define routes for specific HTTP methods. Patterns can include placeholders for path parameters.

```go
r.Get("/users/{id}", userHandler)
```

### Grouping Routes

Group related routes under a common base path using the Group method.

```go
r.Group("/api", func(api *Router) {
   api.Get("/users", apiUsersHandler)
   api.Post("/users", apiCreateUserHandler)
})
```

### Applying Middleware

Apply middleware functions globally or to specific route groups.

```go

// Global middleware
r.Use(loggingMiddleware)

// Middleware for a group
r.Group("/admin", func(admin *Router) {
admin.Use(authMiddleware)
admin.Get("/dashboard", adminDashboardHandler)
})
```

### Custom Handlers for 404 and 405 and 500 Responses

Set custom handlers to provide consistent error responses.

```go
r.NotFound(notFoundHandler)
r.MethodNotAllowed(methodNotAllowedHandler)
r.InternalServerError(internalServerErrorHandler)
```

### Trailing Slash Handling

Configure the router to automatically redirect trailing slashes.

```go
r.RedirectTrailingSlash(true) // Enabled by default
```
### Serving Static Files

Serve files from a directory or serve a single file.

```go
// Serve files from the "./static" directory under "/static/"
fs := http.Dir("./static")
r.ServeFiles("/static/", fs)

// Serve a single file
r.ServeFile("/favicon.ico", "./static/favicon.ico")
```
--- 

## Important Notes

- **Pattern Matching**: Patterns not ending with a slash (/) are treated as exact matches, while patterns ending with a slash are treated as prefix matches.
- **Middleware Order**: Middleware functions are applied in the order they are added, wrapping subsequent middleware and the final handler.
- **Custom 404 and 405 Handling**: The router uses intercepting response writers to capture 404 and 405 responses from the underlying http.ServeMux and invoke custom handlers.
- **Trailing Slash Redirection**: When enabled, requests with trailing slashes are redirected to the same path without the trailing slash.

## Future Improvements

- **Automatic OPTIONS Handling**: Provide automatic handling of OPTIONS requests.
- **Error Handling Enhancements**: Provide mechanisms for handling other HTTP status codes.