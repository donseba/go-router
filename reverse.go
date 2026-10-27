package router

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

var (
	// ErrUnknownRoute is returned for a route name that was never registered.
	ErrUnknownRoute = errors.New("unknown route")

	// ErrMissingParam is returned when a route parameter has no value.
	ErrMissingParam = errors.New("missing route parameter")

	// ErrUnknownParam is returned for a parameter the route does not have.
	ErrUnknownParam = errors.New("unknown route parameter")
)

// hostParams fill the wildcard subdomain of a host route in RouteURL.
var hostParams = []string{"subdomain", "tenant"}

// RoutePathWithParams returns the path of the named route with its parameters
// filled in: {name} with the URL-escaped value and {name...} with the value's
// segments, each URL-escaped, so slashes are kept. A {name...} value is a
// string, split on "/", or a []string of segments.
//
// It returns "" for an unknown route. A parameter without a value is left as
// its placeholder. Both are reported to slog.Default(). Use RoutePathArgs,
// which takes the same map, to get errors instead, and to add a query
// string.
func (r *Router) RoutePathWithParams(name string, params map[string]any) string {
	routeInfo, ok := r.lenientRoute(name)
	if !ok {
		return ""
	}

	return lenientRoutePath(name, routeInfo, params)
}

// tryRoutePath returns the path of the named route with its parameters,
// from a map or key value pairs, filled in, followed by the query string of
// query, if any. The errors are those of RoutePathArgs.
func (r *Router) tryRoutePath(name string, params routeParams, query url.Values) (string, error) {
	routeInfo, prefix, ok := r.reverseRoute(name)
	if !ok {
		return "", fmt.Errorf("router: %w %q", ErrUnknownRoute, name)
	}

	path, err := strictRoutePath(name, routeInfo, prefix, params)
	if err != nil {
		return "", err
	}

	if len(query) == 0 {
		return path, nil
	}

	return path + encodeQuery(query), nil
}

// RouteURL returns the URL of the named route: scheme://host/path for a host
// route, //host/path without a scheme, and just the path for a route without a
// host. A wildcard subdomain is filled from the subdomain or tenant
// parameter. Parameters are handled as in RoutePathWithParams.
func (r *Router) RouteURL(name string, params map[string]any, scheme string) string {
	routeInfo, ok := r.lenientRoute(name)
	if !ok {
		return ""
	}

	path := lenientRoutePath(name, routeInfo, params)
	if routeInfo.Host == "" {
		return path
	}

	return joinURL(scheme, routeURLHost(routeInfo.Host, params), path)
}

// namedRoute looks a route name up in the router that resolves r's names:
// the outermost router r is mounted on, which knows the public paths.
// A router mounted with NamePrefix finds its own names with the prefix
// first, and the names of the routers it is mounted on without it. A router
// returned by For gives the public paths of the request it was made for.
func (r *Router) namedRoute(name string) (RouteInfo, bool) {
	routeInfo, ok := r.lookupRoute(name)

	// A router returned by For gives the paths of the request's prefix.
	if ok && r.servedAt != nil {
		routeInfo = r.servedAt.publicRoute(routeInfo)
	}

	return routeInfo, ok
}

// lenientRoute is namedRoute for RoutePathWithParams and RouteURL, which
// report an unknown route to slog.Default() instead of returning an error.
func (r *Router) lenientRoute(name string) (RouteInfo, bool) {
	routeInfo, ok := r.namedRoute(name)
	if !ok {
		slog.Default().Warn("router: unknown route", slog.String("route", name))
	}

	return routeInfo, ok
}

// reverseRoute is namedRoute for filling in a route path, without joining
// the prefix of a router returned by For or At to the route's path first:
// it returns the route as r's root router has it, and the public path to
// put before its path. The root route of such a router is at the prefix
// exactly as given, with nothing to put before it.
func (r *Router) reverseRoute(name string) (routeInfo RouteInfo, prefix string, ok bool) {
	routeInfo, ok = r.lookupRoute(name)
	if !ok || r.servedAt == nil {
		return routeInfo, "", ok
	}

	if routeInfo.Path == "/" {
		routeInfo.Path = r.servedAt.root

		return routeInfo, "", true
	}

	return routeInfo, r.servedAt.path, true
}

// lookupRoute looks a route name up as namedRoute does, and returns the
// route as the router that resolves r's names has it.
func (r *Router) lookupRoute(name string) (RouteInfo, bool) {
	rootRouter, prefix := r.reverseRoot()
	rootRouter.mu.RLock()
	defer rootRouter.mu.RUnlock()

	routeInfo, ok := rootRouter.routes[prefix+name]
	if !ok && prefix != "" {
		routeInfo, ok = rootRouter.routes[name]
	}

	return routeInfo, ok
}

// PathFunc builds the path of a named route from arguments, as
// RoutePathArgs does. Router.RoutePathArgs method values are assignable to
// it, including those of the routers For and At return:
//
//	var paths router.PathFunc = news.At("/en/news").RoutePathArgs
//	paths("show", "slug", "hello") // "/en/news/hello"
//
// Code that only builds links, such as a list rendered under different
// prefixes, can take a PathFunc instead of a *Router.
type PathFunc func(name string, args ...any) (string, error)

// RoutePathArgs is the route template function of FuncMap as a Go function:
// the path of the named route with its parameters filled in, from key value
// pairs, maps of parameters (map[string]any or map[string]string) and
// url.Values for the query string, in any order and combination:
//
//	r.RoutePathArgs("users.show", "id", 42)
//	r.RoutePathArgs("users.posts.show", map[string]any{"id": 1, "post": 2})
//	r.RoutePathArgs("search", url.Values{"q": {"go"}})
//
// Later values replace earlier ones for the same parameter; query values
// are combined. It returns an error wrapping ErrUnknownRoute,
// ErrMissingParam or ErrUnknownParam for an unknown route, a parameter
// without a (non-empty) value, or a parameter the route does not have, and
// an error for a key without a value or an argument of another type. The
// subdomain and tenant parameters of a wildcard subdomain route are
// accepted and ignored.
func (r *Router) RoutePathArgs(name string, args ...any) (string, error) {
	params, query, err := routeArgs(args)
	if err != nil {
		return "", fmt.Errorf("router: route %q: %w", name, err)
	}

	return r.tryRoutePath(name, params, query)
}

// routeArgs returns the parameters and query of RoutePathArgs' arguments:
// key value pairs as they are, read without building a map, a single
// map[string]any as it is, and any other arguments as templateRouteArgs
// turns them into a map and query values.
func routeArgs(args []any) (routeParams, url.Values, error) {
	if keyValuePairs(args) {
		return routeParams{pairs: args}, nil, nil
	}

	if len(args) == 1 {
		if values, ok := args[0].(map[string]any); ok {
			return routeParams{values: values}, nil, nil
		}
	}

	values, query, err := templateRouteArgs(args)

	return routeParams{values: values}, query, err
}

// keyValuePairs reports whether args are key value pairs alone, or none:
// an even number of arguments with a string at every even index.
func keyValuePairs(args []any) bool {
	if len(args)%2 != 0 {
		return false
	}

	for i := 0; i < len(args); i += 2 {
		if _, ok := args[i].(string); !ok {
			return false
		}
	}

	return true
}

// routeParams are the parameters a route path is filled in with: a map,
// or the key value pairs of RoutePathArgs, of which a later value for a key
// replaces an earlier one.
type routeParams struct {
	values map[string]any
	pairs  []any
}

// get returns the value of a parameter.
func (p routeParams) get(name string) (any, bool) {
	if p.pairs == nil {
		value, ok := p.values[name]

		return value, ok
	}

	for i := len(p.pairs) - 2; i >= 0; i -= 2 {
		if p.pairs[i].(string) == name {
			return p.pairs[i+1], true
		}
	}

	return nil, false
}

// count returns the number of parameters, a key given twice counted once.
func (p routeParams) count() int {
	if p.pairs == nil {
		return len(p.values)
	}

	count := 0

	for i := 0; i < len(p.pairs); i += 2 {
		if !p.givenLater(i) {
			count++
		}
	}

	return count
}

// givenLater reports whether the key of the pair at index i is given again
// later.
func (p routeParams) givenLater(i int) bool {
	name := p.pairs[i].(string)

	for j := i + 2; j < len(p.pairs); j += 2 {
		if p.pairs[j].(string) == name {
			return true
		}
	}

	return false
}

// names returns the names of the parameters, each once, in no particular
// order.
func (p routeParams) names() []string {
	if p.pairs == nil {
		return slices.Collect(maps.Keys(p.values))
	}

	names := make([]string, 0, len(p.pairs)/2)

	for i := 0; i < len(p.pairs); i += 2 {
		if !p.givenLater(i) {
			names = append(names, p.pairs[i].(string))
		}
	}

	return names
}

// sizeHint is about how many bytes the parameters add to a path: the
// length of the string values of key value pairs, or a guess for a map.
func (p routeParams) sizeHint() int {
	if p.pairs == nil {
		return 16
	}

	size := 0

	for i := 1; i < len(p.pairs); i += 2 {
		if value, ok := p.pairs[i].(string); ok {
			size += len(value)
		} else {
			size += 8
		}
	}

	return size
}

func lenientRoutePath(name string, routeInfo RouteInfo, params map[string]any) string {
	filled := fillRoutePath("", routeInfo.Path, routeParams{values: params})
	if len(filled.missing) > 0 {
		slog.Default().Warn("router: route path has parameters without a value",
			slog.String("route", name),
			slog.Any("missing", filled.missing),
		)
	}

	return filled.path
}

func strictRoutePath(name string, routeInfo RouteInfo, prefix string, params routeParams) (string, error) {
	filled := fillRoutePath(prefix, routeInfo.Path, params)

	missing := append(filled.missing, filled.empty...)
	if len(missing) > 0 {
		return "", fmt.Errorf("router: route %q: %w %s", name, ErrMissingParam, quoteSorted(missing))
	}

	// Every parameter filled a wildcard: none is unknown.
	if filled.used == params.count() {
		return filled.path, nil
	}

	var unknown []string

	for _, key := range params.names() {
		if hasWildcard(routeInfo.Path, key) {
			continue
		}

		if strings.HasPrefix(routeInfo.Host, "*.") && slices.Contains(hostParams, key) {
			continue
		}

		unknown = append(unknown, key)
	}

	if len(unknown) > 0 {
		return "", fmt.Errorf("router: route %q: %w %s", name, ErrUnknownParam, quoteSorted(unknown))
	}

	return filled.path, nil
}

type filledRoutePath struct {
	path    string
	used    int      // wildcards with a value; a pattern names each once
	missing []string // wildcards without a value, left as placeholders
	empty   []string // single-segment wildcards with an empty value
}

// fillRoutePath fills the wildcards of a ServeMux pattern path, after
// prefix, a public path without wildcards.
func fillRoutePath(prefix string, pattern string, params routeParams) filledRoutePath {
	var (
		filled filledRoutePath
		path   strings.Builder
	)

	path.Grow(len(prefix) + len(pattern) + params.sizeHint())
	path.WriteString(prefix)

	for {
		open := strings.IndexByte(pattern, '{')
		if open < 0 {
			break
		}

		end := strings.IndexByte(pattern[open:], '}')
		if end < 0 {
			break
		}

		end += open
		path.WriteString(pattern[:open])

		placeholder := pattern[open : end+1]
		name, rest := strings.CutSuffix(pattern[open+1:end], "...")
		pattern = pattern[end+1:]

		if name == "$" {
			continue
		}

		value, ok := params.get(name)

		switch {
		case !ok:
			filled.missing = append(filled.missing, name)
			path.WriteString(placeholder)
		case rest:
			writeSegments(&path, value)
		default:
			segment := url.PathEscape(toString(value))
			if segment == "" {
				filled.empty = append(filled.empty, name)
			}

			path.WriteString(segment)
		}

		if ok {
			filled.used++
		}
	}

	path.WriteString(pattern)
	filled.path = path.String()

	return filled
}

// writeSegments writes the value of a {name...} wildcard escaped segment
// by segment, keeping the slashes between them. The value is a []string of
// segments, or a string split on "/".
func writeSegments(path *strings.Builder, value any) {
	if segments, ok := value.([]string); ok {
		for i, segment := range segments {
			if i > 0 {
				path.WriteByte('/')
			}

			path.WriteString(url.PathEscape(segment))
		}

		return
	}

	rest := strings.TrimPrefix(toString(value), "/")

	for {
		segment, after, more := strings.Cut(rest, "/")
		path.WriteString(url.PathEscape(segment))

		if !more {
			return
		}

		path.WriteByte('/')
		rest = after
	}
}

// hasWildcard reports whether a route path has a {name} or {name...}
// wildcard.
func hasWildcard(pattern string, name string) bool {
	if name == "$" {
		return false
	}

	for {
		open := strings.IndexByte(pattern, '{')
		if open < 0 {
			return false
		}

		end := strings.IndexByte(pattern[open:], '}')
		if end < 0 {
			return false
		}

		end += open
		wildcard := strings.TrimSuffix(pattern[open+1:end], "...")

		if wildcard == name {
			return true
		}

		pattern = pattern[end+1:]
	}
}

// encodeQuery returns query as a query string with its "?", or "" when it
// has no values.
func encodeQuery(query url.Values) string {
	if encoded := query.Encode(); encoded != "" {
		return "?" + encoded
	}

	return ""
}

// appendQuery adds the values of query to those of the same keys in
// merged.
func appendQuery(merged url.Values, query url.Values) {
	for key, values := range query {
		merged[key] = append(merged[key], values...)
	}
}

func routeURLHost(host string, params map[string]any) string {
	if !strings.HasPrefix(host, "*.") {
		return host
	}

	for _, key := range hostParams {
		if value, exists := params[key]; exists {
			return url.PathEscape(toString(value)) + strings.TrimPrefix(host, "*")
		}
	}

	return host
}

func joinURL(scheme string, host string, path string) string {
	if scheme == "" {
		return "//" + host + path
	}

	return scheme + "://" + host + path
}

// quoteSorted sorts names and returns them quoted, separated by commas, for
// the errors of route parameters.
func quoteSorted(names []string) string {
	slices.Sort(names)

	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("%q", name)
	}

	return strings.Join(quoted, ", ")
}

// templateRouteArgs turns the arguments of the route template function into
// parameters and query values: key value pairs and maps of parameters, and
// url.Values for the query string.
func templateRouteArgs(args []any) (map[string]any, url.Values, error) {
	var (
		params = make(map[string]any)
		query  url.Values
	)

	for i := 0; i < len(args); i++ {
		switch arg := args[i].(type) {
		case string:
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("parameter %q has no value", arg)
			}

			params[arg] = args[i+1]
			i++
		case map[string]any:
			maps.Copy(params, arg)
		case map[string]string:
			for key, value := range arg {
				params[key] = value
			}
		case url.Values:
			if query == nil {
				query = url.Values{}
			}

			appendQuery(query, arg)
		default:
			return nil, nil, fmt.Errorf("unexpected argument %v (%T): want key value pairs, a map or url.Values", arg, arg)
		}
	}

	return params, query, nil
}

// HasRoute reports whether a route has the given name, so a template or
// handler can leave out a link to a route that may not exist, such as a
// route of an optional module.
func (r *Router) HasRoute(name string) bool {
	_, ok := r.namedRoute(name)

	return ok
}

// IsActiveRoute reports whether current is the path of the named route or a
// path below it, for navigation: a route at /admin/news is active on
// /admin/news and /admin/news/7/edit, but not on /admin/newsletter. A route
// whose path ends in a slash, such as /admin/, is active below it.
//
// current is a *url.URL, a url.URL, an *http.Request (its URL) or a string
// with a path or URL, such as the page URL an htmx request reports. A nil
// URL or request is not active.
//
// The route's wildcards match any value, unless args gives theirs, as key
// value pairs or maps of parameters as for RoutePathArgs; url.Values in args
// are ignored:
//
//	r.IsActiveRoute(req, "users.show")            // on /users/7 and /users/8/posts
//	r.IsActiveRoute(req, "users.show", "id", 7)   // on /users/7, not /users/8
//
// An unknown route is not active and not an error, so navigation can name
// optional routes. It returns an error for invalid args, a parameter the
// route does not have and a current of another type.
func (r *Router) IsActiveRoute(current any, name string, args ...any) (bool, error) {
	return r.isActiveRoute(current, name, false, args)
}

// IsActiveRouteExact is IsActiveRoute without the paths below the route:
// whether current is the path of the named route.
func (r *Router) IsActiveRouteExact(current any, name string, args ...any) (bool, error) {
	return r.isActiveRoute(current, name, true, args)
}

func (r *Router) isActiveRoute(current any, name string, exact bool, args []any) (bool, error) {
	currentURL, err := toCurrentURL(current)
	if err != nil {
		return false, fmt.Errorf("router: active route %q: %w", name, err)
	}

	params, _, err := templateRouteArgs(args)
	if err != nil {
		return false, fmt.Errorf("router: active route %q: %w", name, err)
	}

	routeInfo, ok := r.namedRoute(name)
	if !ok || currentURL == nil {
		return false, nil
	}

	matcher := newRouteMatcher(routeInfo.Path)

	var unknown []string

	for key := range params {
		if !slices.Contains(matcher.wildcards, key) {
			unknown = append(unknown, key)
		}
	}

	if len(unknown) > 0 {
		return false, fmt.Errorf("router: active route %q: %w %s", name, ErrUnknownParam, quoteSorted(unknown))
	}

	return matcher.match(currentURL.EscapedPath(), params, exact), nil
}

// toCurrentURL returns the URL of the current page given to IsActiveRoute,
// nil for a nil URL or request.
func toCurrentURL(current any) (*url.URL, error) {
	switch current := current.(type) {
	case nil:
		return nil, nil
	case *url.URL:
		return current, nil
	case url.URL:
		return &current, nil
	case *http.Request:
		if current == nil {
			return nil, nil
		}

		return current.URL, nil
	case string:
		if current == "" {
			return nil, nil
		}

		parsed, err := url.Parse(current)
		if err != nil {
			return nil, err
		}

		return parsed, nil
	default:
		return nil, fmt.Errorf("unexpected current page %v (%T): want a *url.URL, url.URL, *http.Request or string",
			current, current)
	}
}

// routeMatcher matches request paths against a route path, segment by
// segment, as the ServeMux does: {name} matches one non-empty segment and
// {name...} the rest of the path.
type routeMatcher struct {
	segments  []string // the route path's segments, without the trailing ""
	subtree   bool     // the route path ends in a slash
	wildcards []string
}

func newRouteMatcher(routePath string) routeMatcher {
	var matcher routeMatcher

	trimmed := strings.TrimPrefix(routePath, "/")
	if trimmed == "" || strings.HasSuffix(trimmed, "/") {
		matcher.subtree = true
		trimmed = strings.TrimSuffix(trimmed, "/")
	}

	if trimmed != "" {
		matcher.segments = strings.Split(trimmed, "/")
	}

	for _, segment := range matcher.segments {
		if name, ok := wildcardName(segment); ok {
			matcher.wildcards = append(matcher.wildcards, strings.TrimSuffix(name, "..."))
		}
	}

	return matcher
}

// wildcardName returns the name of a {name} or {name...} segment.
func wildcardName(segment string) (string, bool) {
	if len(segment) < 2 || segment[0] != '{' || segment[len(segment)-1] != '}' {
		return "", false
	}

	return segment[1 : len(segment)-1], true
}

// match reports whether escapedPath is the route's path, or with exact
// false also a path below it, with params as the values of wildcards that
// have one.
func (m routeMatcher) match(escapedPath string, params map[string]any, exact bool) bool {
	path, ok := pathSegments(escapedPath)
	if !ok {
		return false
	}

	for i, segment := range m.segments {
		name, isWildcard := wildcardName(segment)

		if rest, isRest := strings.CutSuffix(name, "..."); isWildcard && isRest {
			return matchRest(path[min(i, len(path)):], params, rest, exact)
		}

		if i >= len(path) || !matchSegment(path[i], segment, params) {
			return false
		}
	}

	return m.matchTail(path[len(m.segments):], exact)
}

// pathSegments returns the unescaped segments of a rooted escaped path,
// and false for a path that is not rooted or not escaped properly.
func pathSegments(escapedPath string) ([]string, bool) {
	trimmed, ok := strings.CutPrefix(escapedPath, "/")
	if !ok {
		return nil, false
	}

	path := strings.Split(trimmed, "/")
	for i, segment := range path {
		unescaped, err := url.PathUnescape(segment)
		if err != nil {
			return nil, false
		}

		path[i] = unescaped
	}

	return path, true
}

// matchSegment reports whether a path segment matches a segment of the
// route path: the same literal, or a {name} wildcard, which matches a
// segment that is not empty, and only its value in params when it has one.
func matchSegment(value string, segment string, params map[string]any) bool {
	name, isWildcard := wildcardName(segment)

	switch {
	case !isWildcard:
		return value == segment
	case value == "":
		return false
	default:
		param, exists := params[name]

		return !exists || toString(param) == value
	}
}

// matchTail reports whether rest, the segments of a path after those of
// the route path, are what the route matches: with exact set, none, or the
// empty segment of a trailing slash for a route path that ends in one;
// otherwise any, and at least the empty one for such a route path.
func (m routeMatcher) matchTail(rest []string, exact bool) bool {
	switch {
	case m.subtree && exact:
		// "/admin/" is the path with one empty segment after the route's.
		return len(rest) == 1 && rest[0] == ""
	case m.subtree:
		return len(rest) >= 1
	case exact:
		return len(rest) == 0
	default:
		return true
	}
}

// matchRest matches the segments a {name...} wildcard covers against its
// value, if it has one.
func matchRest(path []string, params map[string]any, name string, exact bool) bool {
	value, exists := params[name]
	if !exists {
		return true
	}

	want, ok := value.([]string)
	if !ok {
		want = strings.Split(strings.TrimPrefix(toString(value), "/"), "/")
	}

	if len(path) < len(want) || (exact && len(path) != len(want)) {
		return false
	}

	return slices.Equal(path[:len(want)], want)
}
