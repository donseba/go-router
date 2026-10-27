package router

import (
	"net/http"
)

// Option configures a Router made by NewSite.
type Option func(*Router)

// NewSite returns a router for websites: it has its own ServeMux and
// OpenAPI documentation is off, so there is no API title or version to
// give. UseOpenapiDocs can still turn documentation on.
func NewSite(options ...Option) *Router {
	r := New(http.NewServeMux(), "", "")
	r.openapiDocs = false

	for _, option := range options {
		option(r)
	}

	return r
}
