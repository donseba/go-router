package router

import (
	"testing"
)

// TestLocalURL checks that paths on the site are accepted and returned
// parsed, and other addresses, protocol-relative ones and those with a
// backslash or line break, are not.
func TestLocalURL(t *testing.T) {
	tests := []struct {
		target   string
		ok       bool
		path     string
		rawQuery string
	}{
		{target: "/", ok: true, path: "/"},
		{target: "/en/about?x=1#top", ok: true, path: "/en/about", rawQuery: "x=1"},
		{target: "/nl/%C3%BCber", ok: true, path: "/nl/über"},
		{target: "/en/about?path=/a?b", ok: true, path: "/en/about", rawQuery: "path=/a?b"},
		{target: ""},
		{target: "en/about"},
		{target: "//evil.example/"},
		{target: "/\\evil.example"},
		{target: "/a\r\nLocation: x"},
		{target: "https://example.com/"},
		{target: "javascript:alert(1)"},
		{target: "/%zz"},
	}

	for _, tt := range tests {
		parsed, ok := LocalURL(tt.target)
		if ok != tt.ok {
			t.Errorf("LocalURL(%q): got ok %v, want %v", tt.target, ok, tt.ok)

			continue
		}

		if !ok {
			if parsed != nil {
				t.Errorf("LocalURL(%q): got %v for a rejected target", tt.target, parsed)
			}

			continue
		}

		if parsed.Path != tt.path || parsed.RawQuery != tt.rawQuery {
			t.Errorf(
				"LocalURL(%q): got path %q query %q, want %q %q",
				tt.target,
				parsed.Path,
				parsed.RawQuery,
				tt.path,
				tt.rawQuery,
			)
		}
	}

	escaped, _ := LocalURL("/nl/%C3%BCber")
	if escaped.EscapedPath() != "/nl/%C3%BCber" {
		t.Fatalf("EscapedPath: got %q", escaped.EscapedPath())
	}
}
