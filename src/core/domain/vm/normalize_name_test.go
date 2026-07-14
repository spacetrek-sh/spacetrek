package vm

import (
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	// NormalizeName is a pure normalizer: lowercase, replace non-[a-z0-9-]
	// with a hyphen, collapse consecutive hyphens, trim leading/trailing
	// hyphens, truncate to 63 chars. It does NOT reject names starting
	// with a digit — that's a DNS-layer concern, not a normalization one.
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"!!!", ""},
		{"---", ""},
		{"nervous-einstein", "nervous-einstein"},
		{"My VM!", "my-vm"},
		{"My_VM", "my-vm"},
		{"a--b", "a-b"},
		{"  -foo-  ", "foo"},
		{"--foo--", "foo"},
		{"UPPER", "upper"},
		{"MIXED-Case", "mixed-case"},
		// Non-ASCII collapses to hyphen, trims handle edges.
		{"café", "caf"},
		{"foo.bar.baz", "foo-bar-baz"},
		{"foo/bar", "foo-bar"},
		{"a b c", "a-b-c"},
		// Digits are valid mid-name characters. Leading digits are kept by
		// normalization (admin-provided names are expected to be reasonable).
		{"123abc", "123abc"},
		{"abc123", "abc123"},
		{"a1b2c3", "a1b2c3"},
	}
	for _, c := range cases {
		got := NormalizeName(c.in)
		if got != c.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeName_Truncates(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := NormalizeName(long)
	if len(got) != 63 {
		t.Errorf("expected truncation to 63 chars, got %d: %q", len(got), got)
	}
}

func TestNormalizeName_TruncatesWithoutTrailingHyphen(t *testing.T) {
	// Truncation must not leave a trailing hyphen — RFC 1123 host labels
	// must end with an alphanumeric char.
	long := "ab" + strings.Repeat("c", 70)
	got := NormalizeName(long)
	if strings.HasSuffix(got, "-") {
		t.Errorf("NormalizeName returned trailing hyphen after truncation: %q", got)
	}
}
