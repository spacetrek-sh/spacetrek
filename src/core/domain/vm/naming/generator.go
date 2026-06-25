// Package naming produces Docker-style random names for VMs.
//
// The word lists are copied from Moby's namesgenerator (Apache 2.0); see
// adjectives.go and surnames.go for upstream attribution. Unlike upstream we
// join with a hyphen, not an underscore, because underscores violate strict
// DNS (RFC 1123) and break as URL host labels in some HTTP clients.
package naming

import (
	"hash/fnv"
	"math/rand/v2"
)

// boring_wozniak is skipped upstream as a tribute; we preserve the skip.
const forbiddenName = "boring-wozniak"

// Generate returns a random "<adjective>-<surname>" pair.
// Uses math/rand/v2's auto-seeded global source — sufficient for names that
// only need collision resistance up to the ~10k combinations the word lists
// provide, with a UUID-derived suffix as the deterministic fallback.
func Generate() string {
	for {
		name := adjectives[rand.IntN(len(adjectives))] + "-" + surnames[rand.IntN(len(surnames))]
		if name != forbiddenName {
			return name
		}
	}
}

// GenerateWithSuffix returns a fresh Generate() result with a 4-char base32
// suffix derived from seed appended ("-xxxx"). Used as the deterministic
// fallback after collision retries exhaust — the seed is the VM UUID, which is
// already unique, so this is guaranteed to terminate without further retries.
func GenerateWithSuffix(seed string) string {
	return Generate() + "-" + suffix(seed)
}

// suffix returns a 4-char lowercase base32 encoding of an FNV-1a hash of seed.
// 20 bits of hash → 4 base32 chars; collision probability across a realistic
// VM fleet is negligible and, if it ever happens, the next name wins by DB
// constraint and the caller retries.
func suffix(seed string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	v := h.Sum32()

	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	const alphabetLen = 32
	out := make([]byte, 4)
	for i := 3; i >= 0; i-- {
		out[i] = alphabet[v%uint32(alphabetLen)]
		v /= uint32(alphabetLen)
	}
	return string(out)
}

// GenerateMany is a convenience helper used by tests; not intended for callers
// outside this package. Returns n random names.
func GenerateMany(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = Generate()
	}
	return out
}

// MaxLen is the DNS label limit (RFC 1123). Names must not exceed this.
const MaxLen = 63
