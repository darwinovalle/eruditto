package hash

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestBytes_KnownVector checks the canonical SHA-256("abc") test vector.
// This is the FIPS-180-4 reference vector, the same one every SHA-256
// implementation is tested against. If this ever fails, the most likely
// causes are (a) we accidentally swapped algorithms, or (b) the Go
// standard library's sha256 package is broken (it isn't).
func TestBytes_KnownVector(t *testing.T) {
	got := Bytes([]byte("abc"))
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Errorf("Bytes(\"abc\") = %q, want %q", got, want)
	}
}

// TestBytes_EmptyInput pins the SHA-256 of the empty string. This is
// the most stable test vector in cryptography: every implementation
// that ever shipped has produced this exact value.
func TestBytes_EmptyInput(t *testing.T) {
	got := Bytes(nil)
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want {
		t.Errorf("Bytes(nil) = %q, want %q", got, want)
	}
	// Also exercise the empty-slice path to make sure nil and
	// []byte{} produce the same output (they must, but assert it
	// explicitly because some buggy implementations distinguish).
	// NOTE: got2 is declared at function scope (not inside the if
	// init clause) so we can compare it with `got` afterward.
	// Go scopes variables declared in `if init; cond` to the if
	// body only, so this kind of cross-statement use requires
	// hoisting the declaration.
	got2 := Bytes([]byte{})
	if got2 != want {
		t.Errorf("Bytes([]byte{}) = %q, want %q", got2, want)
	}
	if got != got2 {
		t.Error("Bytes(nil) and Bytes([]byte{}) differ")
	}
}

// TestString_KnownVector checks the same FIPS vector but through
// the String entry point.
func TestString_KnownVector(t *testing.T) {
	got := String("abc")
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Errorf("String(\"abc\") = %q, want %q", got, want)
	}
}

// TestStringAndBytesAgree is the test the checklist explicitly asks
// for: hash.String(s) and hash.Bytes([]byte(s)) must produce identical
// output for any s. We run this against a handful of inputs covering
// ASCII, multi-byte UTF-8, embedded NULs, and a moderately long string.
func TestStringAndBytesAgree(t *testing.T) {
	inputs := []string{
		"",                           // empty
		"abc",                        // short ASCII
		"hello world",                // spaces
		"caf\u00e9 au lait",          // Latin-1 supplement (é)
		"\u4f60\u597d",               // CJK (你好)
		"\U0001F44D",                 // emoji (👍)
		"line1\nline2\rline3\tline4", // control characters
		"a\x00b\x00c",                // embedded NULs
		strings.Repeat("x", 1024),    // 1 KiB
		strings.Repeat("ab", 4096),   // 8 KiB
	}
	for _, s := range inputs {
		t.Run(s, func(t *testing.T) {
			gotString := String(s)
			gotBytes := Bytes([]byte(s))
			if gotString != gotBytes {
				t.Errorf("String/Bytes mismatch:\n  String = %q\n  Bytes  = %q", gotString, gotBytes)
			}
		})
	}
}

// TestLargeInput makes sure the function handles arbitrary input
// length without truncation or buffer mishaps. We use 1 MiB of
// crypto-random bytes; we don't care about the exact value, just
// that (a) it runs to completion, (b) the output has the expected
// 64-character shape, and (c) the output matches what crypto/sha256
// itself produces on the same input.
func TestLargeInput(t *testing.T) {
	const size = 1 << 20 // 1 MiB
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	got := Bytes(data)
	if len(got) != 64 {
		t.Errorf("len(Bytes(data)) = %d, want 64", len(got))
	}
	// Compare against the standard library directly. If we ever
	// replaced our hexEncode helper with encoding/hex, this is
	// where the test would still pass; it's a contract test, not
	// an implementation test.
	wantRaw := sha256.Sum256(data)
	want := hex.EncodeToString(wantRaw[:])
	if got != want {
		t.Errorf("Bytes(1MiB) differs from crypto/sha256:\n  got  = %q\n  want = %q", got[:16]+"…", want[:16]+"…")
	}
}

// TestOutputShape asserts the structural property of every output:
// exactly 64 lowercase hex characters, no uppercase, no padding.
// This is the property callers rely on (e.g., when storing hashes
// in a UNIQUE column in SQLite).
func TestOutputShape(t *testing.T) {
	got := Bytes([]byte("anything"))
	if len(got) != 64 {
		t.Errorf("len = %d, want 64", len(got))
	}
	for i, c := range got {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("character at index %d is %q, want lowercase hex", i, c)
		}
	}
}
