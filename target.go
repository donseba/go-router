package router

import (
	"net/url"
	"strings"
)

// LocalURL reports whether target, such as a ?next= or back value from a
// form, is a path on this site that can be redirected to without an open
// redirect: it starts with a single slash, has no scheme or host, and no
// backslash or line break, which browsers may read as another host. It
// returns the target parsed when it is, so its Path, RawQuery and
// EscapedPath can be read without parsing it again:
//
//	page, ok := router.LocalURL(req.FormValue("path"))
//	if !ok {
//		page = &url.URL{Path: "/"}
//	}
//	preview.Path, preview.RawQuery = page.Path, page.RawQuery
//
// middleware.SameOriginReferer does the same for the Referer.
func LocalURL(target string) (*url.URL, bool) {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.ContainsAny(target, "\\\r\n") {
		return nil, false
	}

	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return nil, false
	}

	return parsed, true
}
