package naming

import (
	"regexp"
	"strings"
	"testing"
)

// dnsLabel matches RFC 1123 host labels: lowercase alphanumeric and hyphens,
// must start with a letter, end with alphanumeric, ≤63 chars.
var dnsLabel = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

func TestGenerate_RFC1123Conformant(t *testing.T) {
	const draws = 10_000
	seen := make(map[string]struct{}, draws)
	maxLen := 0
	for i := 0; i < draws; i++ {
		name := Generate()
		if !dnsLabel.MatchString(name) {
			t.Fatalf("name %q does not match RFC 1123 host label", name)
		}
		if len(name) > MaxLen {
			t.Fatalf("name %q exceeds %d chars: %d", name, MaxLen, len(name))
		}
		if len(name) > maxLen {
			maxLen = len(name)
		}
		seen[name] = struct{}{}
	}

	// Combinatoric ceiling: adjectives*surnames - 1 forbidden.
	ceiling := len(adjectives)*len(surnames) - 1
	if ceiling <= 0 {
		t.Fatalf("word lists are empty")
	}

	// With 10k draws against ~22k combinations, we expect heavy collisions
	// (birthday paradox) but the *name space* should cover well beyond 10k
	// unique pairs over time. We only assert that the word lists combined
	// are large enough to make this a useful namespace.
	if ceiling < 5000 {
		t.Fatalf("namespace too small: %d combinations", ceiling)
	}

	// We must never return the forbidden tribute name.
	if _, ok := seen[forbiddenName]; ok {
		t.Fatalf("forbidden name %q was returned", forbiddenName)
	}
	t.Logf("drew %d names, %d unique, max length %d, ceiling %d", draws, len(seen), maxLen, ceiling)
}

func TestGenerate_HyphenNotUnderscore(t *testing.T) {
	for i := 0; i < 1000; i++ {
		name := Generate()
		if strings.Contains(name, "_") {
			t.Fatalf("name %q contains an underscore", name)
		}
		if !strings.Contains(name, "-") {
			t.Fatalf("name %q has no hyphen separator", name)
		}
	}
}

func TestGenerateWithSuffix_Shape(t *testing.T) {
	for _, seed := range []string{"7c2b1f4a-1234-5678-9abc-def012345678", "x", ""} {
		name := GenerateWithSuffix(seed)
		// <adjective>-<surname>-<4 base32 chars>
		parts := strings.Split(name, "-")
		if len(parts) != 3 {
			t.Fatalf("name %q should have 3 hyphen-separated parts, got %d", name, len(parts))
		}
		suf := parts[2]
		if len(suf) != 4 {
			t.Fatalf("suffix %q should be 4 chars", suf)
		}
		for _, r := range suf {
			if !((r >= 'a' && r <= 'z') || (r >= '2' && r <= '7')) {
				t.Fatalf("suffix %q has non-base32 char %q", suf, r)
			}
		}
		if !dnsLabel.MatchString(name) {
			t.Fatalf("suffixed name %q is not RFC 1123 conformant", name)
		}
	}
}

func TestGenerateWithSuffix_DeterministicSuffixForSameSeed(t *testing.T) {
	// The suffix is a pure function of seed; two invocations with the same
	// seed produce the same suffix (the prefix may differ because of the
	// random adjective-surname pick, but the suffix must match).
	seed := "abc-123"
	suf1 := strings.Split(GenerateWithSuffix(seed), "-")[2]
	suf2 := strings.Split(GenerateWithSuffix(seed), "-")[2]
	if suf1 != suf2 {
		t.Fatalf("suffix for seed %q is not deterministic: %q vs %q", seed, suf1, suf2)
	}
}

func TestGenerateWithSuffix_DifferentSeedsDifferentSuffixUsually(t *testing.T) {
	// 4 base32 chars = 20 bits = ~1M slots. Two distinct seeds colliding
	// is possible but unlikely across a modest sample.
	const samples = 200
	seen := make(map[string]struct{}, samples)
	for i := 0; i < samples; i++ {
		seed := string(rune('a' + i))
		suf := strings.Split(GenerateWithSuffix(seed), "-")[2]
		seen[suf] = struct{}{}
	}
	if len(seen) < samples*9/10 {
		t.Fatalf("suffix collisions too high: %d unique out of %d", len(seen), samples)
	}
}
