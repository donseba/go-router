// Package router is a router for websites and APIs on top of the ServeMux
// of net/http and its method-aware patterns. It adds route groups,
// middleware for routes and for every request, host and subdomain routing,
// named routes with reverse routing, mounted routers, not found pages,
// route matching and OpenAPI helpers. Routes are registered at startup:
// registration freezes on the first request. See
// https://docs.gowebthings.com/go-router for the documentation.
package router
