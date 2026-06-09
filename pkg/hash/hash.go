// Package hash provides SHA-256 hashing helpers used by Eruditto to
// deduplicate clipboard entries.
//
// The package is intentionally tiny: one algorithm (SHA-256), one
// output format (lowercase hex), and two entry points (Bytes and
// String) that are guaranteed to produce identical output for
// equivalent input.
//
// Hashing is used purely for content-based deduplication. It is NOT
// used for security, authentication, or any adversarial setting.
// If you ever need to compare hashes against attacker-controlled
// data, do not extend this package — use a keyed MAC instead.
package hash

import (
	"crypto/sha256"
)

// Bytes returns the lowercase hex-encoded SHA-256 digest of data.
//
// The function is safe to call from multiple goroutines. Each call
// uses its own hasher state and does not share any mutable state
// across calls.
func Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	// %x formats a byte slice as lowercase hex, two characters per byte.
	// sha256.Size is 32, so the result is always 64 hex characters.
	return hexEncode(sum[:])
}

// String returns the lowercase hex-encoded SHA-256 digest of s.
//
// It is exactly equivalent to Bytes([]byte(s)) — the two functions
// are guaranteed to produce identical output for inputs that have
// the same byte representation. The string-to-byte conversion is
// a free view (no allocation) because Go strings are immutable
// byte sequences.
func String(s string) string {
	return Bytes([]byte(s))
}

// hexEncode formats b as lowercase hex. It is a tiny private helper
// to keep the call sites in Bytes and String readable. We avoid
// encoding/hex because it would force every call to allocate a
// destination buffer; for our 32-byte digests the difference is
// negligible, but a tight loop here matches Go's stdlib idioms
// (sha256.Sum256 itself avoids encoding/hex).
func hexEncode(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hex[c>>4]
		out[i*2+1] = hex[c&0x0f]
	}
	return string(out)
}
