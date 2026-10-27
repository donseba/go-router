package middleware

import (
	"slices"
	"testing"
)

func TestParseCSP(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		want   string
	}{
		{
			name:   "keeps order",
			policy: "script-src 'self'; default-src 'none'; img-src 'self' data:",
			want:   "script-src 'self'; default-src 'none'; img-src 'self' data:",
		},
		{
			name:   "lower-cases names and normalizes white space",
			policy: "  Script-SRC\t'self'   https://cdn.example ;;  ; UPGRADE-INSECURE-REQUESTS ",
			want:   "script-src 'self' https://cdn.example; upgrade-insecure-requests",
		},
		{
			name:   "keeps the first of duplicate directives",
			policy: "script-src 'self'; script-src 'unsafe-inline'",
			want:   "script-src 'self'",
		},
		{
			name:   "skips invalid names",
			policy: "scr!pt-src 'self'; img-src data:",
			want:   "img-src data:",
		},
		{
			name:   "removes duplicate sources",
			policy: "script-src 'self' 'SELF' 'sha256-abc' 'sha256-ABC' 'sha256-abc'",
			want:   "script-src 'self' 'sha256-abc' 'sha256-ABC'",
		},
		{
			name:   "empty",
			policy: " ; ",
			want:   "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ParseCSP(test.policy).String(); got != test.want {
				t.Errorf("ParseCSP(%q) = %q, want %q", test.policy, got, test.want)
			}
		})
	}
}

func TestSplitCSP(t *testing.T) {
	got := SplitCSP([]string{"default-src 'self', frame-ancestors 'none'", " ", "img-src data:"})
	want := []string{"default-src 'self'", "frame-ancestors 'none'", "img-src data:"}

	if !slices.Equal(got, want) {
		t.Errorf("SplitCSP = %q, want %q", got, want)
	}
}

func TestCSPPolicySet(t *testing.T) {
	policy := ParseCSP("default-src 'self'; script-src 'self'; img-src data:")

	policy.Set("SCRIPT-SRC", "'none'", "'none'")
	policy.Set("frame-ancestors", "'self'")
	policy.Set("upgrade-insecure-requests")
	policy.Set("bad name", "'self'")

	want := "default-src 'self'; script-src 'none'; img-src data:; frame-ancestors 'self'; upgrade-insecure-requests"
	if got := policy.String(); got != want {
		t.Errorf("policy = %q, want %q", got, want)
	}
}

func TestCSPPolicyAdd(t *testing.T) {
	tests := []struct {
		name      string
		policy    string
		directive string
		sources   []string
		want      string
	}{
		{
			name:      "appends to a directive",
			policy:    "script-src 'self'",
			directive: "script-src",
			sources:   []string{"'sha256-abc'", "'self'", "'sha256-abc'"},
			want:      "script-src 'self' 'sha256-abc'",
		},
		{
			name:      "starts a missing directive from default-src",
			policy:    "default-src 'self' https://cdn.example; img-src data:",
			directive: "script-src",
			sources:   []string{"'sha256-abc'"},
			want:      "default-src 'self' https://cdn.example; img-src data:; script-src 'self' https://cdn.example 'sha256-abc'",
		},
		{
			name:      "follows the fallback chain in order",
			policy:    "default-src 'none'; script-src 'self'",
			directive: "script-src-elem",
			sources:   []string{"'sha256-abc'"},
			want:      "default-src 'none'; script-src 'self'; script-src-elem 'self' 'sha256-abc'",
		},
		{
			name:      "worker-src falls back to child-src first",
			policy:    "default-src 'none'; script-src 'self'; child-src blob:",
			directive: "worker-src",
			sources:   []string{"https://w.example"},
			want:      "default-src 'none'; script-src 'self'; child-src blob:; worker-src blob: https://w.example",
		},
		{
			name:      "starts empty without a fallback",
			policy:    "default-src 'self'",
			directive: "frame-ancestors",
			sources:   []string{"https://parent.example"},
			want:      "default-src 'self'; frame-ancestors https://parent.example",
		},
		{
			name:      "replaces 'none'",
			policy:    "default-src 'none'",
			directive: "style-src",
			sources:   []string{"'sha256-abc'"},
			want:      "default-src 'none'; style-src 'sha256-abc'",
		},
		{
			name:      "adding 'none' to sources changes nothing",
			policy:    "script-src 'self'",
			directive: "script-src",
			sources:   []string{"'none'"},
			want:      "script-src 'self'",
		},
		{
			name:      "adding 'none' to an empty policy sets it",
			policy:    "",
			directive: "object-src",
			sources:   []string{"'none'"},
			want:      "object-src 'none'",
		},
		{
			name:      "drops 'none' among added sources",
			policy:    "script-src 'self'",
			directive: "script-src",
			sources:   []string{"'none'", "'sha256-abc'"},
			want:      "script-src 'self' 'sha256-abc'",
		},
		{
			name:      "keywords compare without case, hashes with case",
			policy:    "script-src 'Self' 'sha256-abc'",
			directive: "script-src",
			sources:   []string{"'self'", "'sha256-ABC'"},
			want:      "script-src 'Self' 'sha256-abc' 'sha256-ABC'",
		},
		{
			name:      "skips sources that would break the header",
			policy:    "script-src 'self'",
			directive: "script-src",
			sources:   []string{"", "a b", "x;img-src *", "y,z"},
			want:      "script-src 'self'",
		},
		{
			name:      "adds a directive without sources",
			policy:    "default-src 'self'",
			directive: "upgrade-insecure-requests",
			want:      "default-src 'self'; upgrade-insecure-requests",
		},
		{
			name:      "keeps 'none' when nothing is added",
			policy:    "script-src 'none'",
			directive: "script-src",
			want:      "script-src 'none'",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := ParseCSP(test.policy)
			policy.Add(test.directive, test.sources...)

			if got := policy.String(); got != test.want {
				t.Errorf("policy = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCSPPolicyOverride(t *testing.T) {
	base := ParseCSP("default-src 'self'; frame-ancestors 'self'; img-src 'self'")

	overridden := base.Clone()
	overridden.Override(ParseCSP("frame-ancestors 'none'; script-src 'sha256-abc'"))

	want := "default-src 'self'; frame-ancestors 'none'; img-src 'self'; script-src 'sha256-abc'"
	if got := overridden.String(); got != want {
		t.Errorf("Override = %q, want %q", got, want)
	}

	if got := base.String(); got != "default-src 'self'; frame-ancestors 'self'; img-src 'self'" {
		t.Errorf("a clone changed the original: %q", got)
	}
}

func TestCSPPolicyAccessors(t *testing.T) {
	var policy CSPPolicy

	if !policy.IsZero() || policy.String() != "" {
		t.Fatalf("zero policy = %q", policy.String())
	}

	policy.Add("img-src", "data:")
	policy.Set("script-src", "'self'")

	sources, ok := policy.Sources("IMG-SRC")
	if !ok || !slices.Equal(sources, []string{"data:"}) {
		t.Fatalf("Sources = %q, %v", sources, ok)
	}

	sources[0] = "changed"

	if again, _ := policy.Sources("img-src"); again[0] != "data:" {
		t.Errorf("Sources shares its slice")
	}

	if _, ok := policy.Sources("style-src"); ok {
		t.Errorf("Sources follows fallbacks")
	}

	policy.Delete("IMG-SRC")
	policy.Delete("missing")

	if got := policy.String(); got != "script-src 'self'" {
		t.Errorf("after Delete = %q", got)
	}
}

func TestStrictCSPParses(t *testing.T) {
	policy := ParseCSP(StrictCSP)

	if policy.String() != StrictCSP {
		t.Errorf("StrictCSP does not round-trip: %q", policy.String())
	}

	for _, name := range []string{"default-src", "script-src", "style-src", "object-src", "base-uri", "form-action", "frame-ancestors"} {
		if _, ok := policy.Sources(name); !ok {
			t.Errorf("StrictCSP lacks %s", name)
		}
	}
}
