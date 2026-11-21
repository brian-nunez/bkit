package objex

import (
	"errors"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// keyViolation reports the first documented ValidateKey rule that key
// breaks, or "" when key satisfies every rule. It restates the rules from
// the ValidateKey doc comment independently of the implementation, so the
// fuzz target can compare the two.
func keyViolation(key string) string {
	if key == "" {
		return "empty"
	}
	if len(key) > MaxKeyLength {
		return "longer than MaxKeyLength"
	}
	if !utf8.ValidString(key) {
		return "invalid UTF-8"
	}
	if strings.ContainsFunc(key, unicode.IsControl) {
		return "control character"
	}
	if strings.Contains(key, `\`) {
		return "backslash"
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "empty, \".\", or \"..\" segment"
		}
	}
	return ""
}

func FuzzValidateKey(f *testing.F) {
	seeds := []string{
		"",
		"a",
		"dir/sub/file.txt",
		".hidden",
		"a..b",
		"...",
		"/",
		"//",
		"/leading",
		"trailing/",
		"a//b",
		".",
		"..",
		"./a",
		"../a",
		"a/.",
		"a/..",
		"a/./b",
		"a/../b",
		"../../etc/passwd",
		`..\..\windows`,
		`back\slash`,
		"nul\x00byte",
		"\x00",
		"tab\tsep",
		"new\nline",
		"carriage\rreturn",
		"esc\x1b[0m",
		"del\x7f",
		"\xff",
		"\xc0\xaf",
		"\xc0\xae\xc0\xae/",
		"\xed\xa0\x80",
		"trunc\xe6\x97",
		"unicode/日本語.txt",
		"cafe\u0301",
		"caf\u00e9",
		"\ufeffbom",
		"zero\u200bwidth",
		"rtl\u202eoverride",
		"full\uff0fwidth",
		"dot\u2024leader",
		"fraction\u2044slash",
		"\U0001f600",
		strings.Repeat("k", MaxKeyLength),
		strings.Repeat("k", MaxKeyLength+1),
		strings.Repeat("k", MaxKeyLength-1) + "é",
		strings.Repeat("k", MaxKeyLength-2) + "é",
		strings.Repeat("a/", MaxKeyLength/2-1) + "zz",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, key string) {
		err := ValidateKey(key)
		violation := keyViolation(key)

		if err != nil {
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("ValidateKey(%q) = %v, want an error matching ErrInvalidKey", key, err)
			}
			if violation == "" {
				t.Fatalf("ValidateKey(%q) = %v, but the key satisfies every documented rule", key, err)
			}
			return
		}
		if violation != "" {
			t.Fatalf("ValidateKey(%q) = nil, but the key breaks a documented rule: %s", key, violation)
		}
	})
}

// isBucketNameByte reports whether c is a lowercase letter, digit, hyphen,
// or dot. isEdge restricts it to the letters and digits allowed at the
// first and last positions.
func isBucketNameByte(c byte, isEdge bool) bool {
	if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
		return true
	}
	return !isEdge && (c == '-' || c == '.')
}

// isDottedQuad reports whether name is four dot-separated groups of one to
// three digits, the shape of an IPv4 address.
func isDottedQuad(name string) bool {
	groups := strings.Split(name, ".")
	if len(groups) != 4 {
		return false
	}
	for _, group := range groups {
		if group == "" || len(group) > 3 || strings.Trim(group, "0123456789") != "" {
			return false
		}
	}
	return true
}

// bucketNameViolation reports the first documented ValidateBucketName rule
// that name breaks, or "" when name satisfies every rule.
func bucketNameViolation(name string) string {
	if len(name) < 3 || len(name) > 63 {
		return "length outside 3 to 63"
	}
	for i := range len(name) {
		if !isBucketNameByte(name[i], i == 0 || i == len(name)-1) {
			return "character set, or first or last character"
		}
	}
	if strings.Contains(name, "..") {
		return "consecutive dots"
	}
	if isDottedQuad(name) {
		return "formatted like an IPv4 address"
	}
	return ""
}

func FuzzValidateBucketName(f *testing.F) {
	seeds := []string{
		"",
		"a",
		"ab",
		"abc",
		"my-bucket",
		"my.bucket.name",
		"a-b",
		"a.b",
		"a..b",
		"a.-b",
		"a-.b",
		"-ab",
		"ab-",
		".ab",
		"ab.",
		"ABC",
		"aBc",
		"a_b",
		"a b",
		"a/b",
		"a\x00b",
		"café",
		"\xff\xfe\xfd",
		"192.168.0.1",
		"1.2.3.4",
		"999.999.999.999",
		"1.2.3",
		"1.2.3.4.5",
		"1.2.3.4a",
		"1234.2.3.4",
		"01.02.03.04",
		"xn--bucket",
		"bucket-s3alias",
		strings.Repeat("a", 63),
		strings.Repeat("a", 64),
		strings.Repeat("a", 62) + "-",
		"a" + strings.Repeat(".b", 31),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		err := ValidateBucketName(name)
		violation := bucketNameViolation(name)

		if err != nil {
			if !errors.Is(err, ErrInvalidBucketName) {
				t.Fatalf("ValidateBucketName(%q) = %v, want an error matching ErrInvalidBucketName", name, err)
			}
			if violation == "" {
				t.Fatalf("ValidateBucketName(%q) = %v, but the name satisfies every documented rule", name, err)
			}
			return
		}
		if violation != "" {
			t.Fatalf("ValidateBucketName(%q) = nil, but the name breaks a documented rule: %s", name, violation)
		}
	})
}

// metadataViolation reports the first documented WithMetadata rule that m
// breaks, or "" when m satisfies every rule: keys are non-empty ASCII
// letters, digits, and hyphens, values are printable ASCII, and no two keys
// differ only in case.
func metadataViolation(m map[string]string) string {
	lowered := make(map[string]bool, len(m))
	for key, value := range m {
		if key == "" {
			return "empty key"
		}
		if strings.ContainsFunc(key, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-'
		}) {
			return "key character outside letters, digits, and hyphens"
		}
		for i := range len(value) {
			if value[i] < 0x20 || value[i] > 0x7e {
				return "value byte outside printable ASCII"
			}
		}
		lower := strings.ToLower(key)
		if lowered[lower] {
			return "keys differ only in case"
		}
		lowered[lower] = true
	}
	return ""
}

// checkNormalizedMetadata asserts that got is the normalized form of in:
// the same entries with lowercase keys made only of lowercase letters,
// digits, and hyphens, and values that are printable ASCII and unchanged.
func checkNormalizedMetadata(t *testing.T, in, got map[string]string) {
	t.Helper()
	if len(got) != len(in) {
		t.Fatalf("normalized metadata %q has %d entries, want %d from %q", got, len(got), len(in), in)
	}
	for key, value := range in {
		lower := strings.ToLower(key)
		normalized, ok := got[lower]
		if !ok {
			t.Fatalf("normalized metadata %q lacks key %q for input key %q", got, lower, key)
		}
		if normalized != value {
			t.Fatalf("normalized metadata[%q] = %q, want %q", lower, normalized, value)
		}
	}
	for key, value := range got {
		for i := range len(key) {
			c := key[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				t.Fatalf("normalized metadata key %q holds byte %q", key, c)
			}
		}
		for i := range len(value) {
			if value[i] < 0x20 || value[i] > 0x7e {
				t.Fatalf("normalized metadata value %q holds byte %q", value, value[i])
			}
		}
	}
}

func FuzzResolveMetadata(f *testing.F) {
	type seed struct{ k1, v1, k2, v2 string }
	seeds := []seed{
		{"owner", "objex", "kind", "test"},
		{"Owner", "objex", "Content-Language", "en"},
		{"Key", "a", "key", "b"},
		{"KEY", "a", "kEy", "a"},
		{"same", "a", "same", "b"},
		{"x-amz-meta", "v", "X-AMZ-META", "v"},
		{"", "v", "k", "v"},
		{"has space", "v", "k", "v"},
		{"under_score", "v", "k", "v"},
		{"dot.key", "v", "k", "v"},
		{"k\x00", "v", "k", "v"},
		{"café", "v", "k", "v"},
		{"\u212a", "kelvin", "k", "v"},
		{"k", "line\nbreak", "j", "v"},
		{"k", "tab\there", "j", "v"},
		{"k", "del\x7f", "j", "v"},
		{"k", "non-ascii é", "j", "v"},
		{"k", "\xff", "j", "v"},
		{"k", "", "j", " ~!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}"},
		{"0", "v", "-", "v"},
		{strings.Repeat("A", 256), strings.Repeat("v", 1024), "b", ""},
	}
	for _, s := range seeds {
		f.Add(s.k1, s.v1, s.k2, s.v2)
	}

	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		in := map[string]string{k1: v1, k2: v2}
		original := maps.Clone(in)
		violation := metadataViolation(in)

		put, putErr := ResolvePutOptions(WithMetadata(in))
		copied, copyErr := ResolveCopyOptions(WithMetadata(in))

		if !maps.Equal(in, original) {
			t.Fatalf("resolving options changed the caller's map from %q to %q", original, in)
		}
		if (putErr == nil) != (copyErr == nil) {
			t.Fatalf("ResolvePutOptions error = %v, ResolveCopyOptions error = %v for %q; want agreement", putErr, copyErr, in)
		}

		if putErr != nil {
			if !errors.Is(putErr, ErrInvalidOption) || !errors.Is(copyErr, ErrInvalidOption) {
				t.Fatalf("errors = %v, %v for %q, want errors matching ErrInvalidOption", putErr, copyErr, in)
			}
			if violation == "" {
				t.Fatalf("metadata %q was rejected with %v, but it satisfies every documented rule", in, putErr)
			}
			return
		}
		if violation != "" {
			t.Fatalf("metadata %q was accepted, but it breaks a documented rule: %s", in, violation)
		}
		if !copied.ReplaceMetadata {
			t.Fatalf("ResolveCopyOptions(WithMetadata(%q)).ReplaceMetadata = false, want true", in)
		}
		checkNormalizedMetadata(t, in, put.Metadata)
		checkNormalizedMetadata(t, in, copied.Metadata)
	})
}

// Generated keys, names, and metadata draw from these alphabets.
const (
	lowerAlphanumeric = "abcdefghijklmnopqrstuvwxyz0123456789"
	keyCharacters     = lowerAlphanumeric + "ABCDEFGHIJKLMNOPQRSTUVWXYZ-_.~ !$&'()*+,;=:@%"
	metadataKeyChars  = lowerAlphanumeric + "ABCDEFGHIJKLMNOPQRSTUVWXYZ-"
)

// keyRunes adds multibyte runes, including a combining mark and an emoji,
// to the ASCII key alphabet.
var keyRunes = []rune(keyCharacters + "éß日本語\u0301\u00a0\U0001f600")

const propertyIterations = 2000

func pick(rng *rand.Rand, alphabet string) byte {
	return alphabet[rng.IntN(len(alphabet))]
}

// randomSegment returns a non-empty key segment that is neither "." nor "..".
func randomSegment(rng *rand.Rand) string {
	for {
		var b strings.Builder
		for range 1 + rng.IntN(12) {
			b.WriteRune(keyRunes[rng.IntN(len(keyRunes))])
		}
		if s := b.String(); s != "." && s != ".." {
			return s
		}
	}
}

// randomKeySegments returns the segments of a valid key at most
// MaxKeyLength bytes long once joined with "/".
func randomKeySegments(rng *rand.Rand) []string {
	segments := []string{randomSegment(rng)}
	length := len(segments[0])
	for range rng.IntN(8) {
		segment := randomSegment(rng)
		if length+1+len(segment) > MaxKeyLength {
			break
		}
		segments = append(segments, segment)
		length += 1 + len(segment)
	}
	return segments
}

// insertAtRuneBoundary inserts s into key at a random rune boundary, so the
// insertion is the only change to the key's validity.
func insertAtRuneBoundary(rng *rand.Rand, key, s string) string {
	boundaries := []int{len(key)}
	for i := range key {
		boundaries = append(boundaries, i)
	}
	at := boundaries[rng.IntN(len(boundaries))]
	return key[:at] + s + key[at:]
}

// forbidKey breaks exactly one documented ValidateKey rule in a valid key
// built from segments, and names the rule it broke.
func forbidKey(rng *rand.Rand, segments []string) (key, rule string) {
	joined := strings.Join(segments, "/")
	switch rng.IntN(8) {
	case 0:
		c := rune(rng.IntN(0x20))
		switch rng.IntN(4) {
		case 0:
			c = 0x7f
		case 1:
			c = rune(0x80 + rng.IntN(0x20))
		}
		return insertAtRuneBoundary(rng, joined, string(c)), "control character"
	case 1:
		return insertAtRuneBoundary(rng, joined, `\`), "backslash"
	case 2:
		invalid := []string{"\xff", "\xc0\xaf", "\xed\xa0\x80", "\xe6\x97", "\x80"}
		return insertAtRuneBoundary(rng, joined, invalid[rng.IntN(len(invalid))]), "invalid UTF-8"
	case 3:
		i := rng.IntN(len(segments) + 1)
		return strings.Join(slices.Insert(slices.Clone(segments), i, ""), "/"), "empty segment"
	case 4:
		dots := []string{".", ".."}[rng.IntN(2)]
		replaced := slices.Clone(segments)
		replaced[rng.IntN(len(replaced))] = dots
		return strings.Join(replaced, "/"), "dot segment"
	case 5:
		return "/" + joined, "leading slash"
	case 6:
		return joined + "/", "trailing slash"
	default:
		return joined + strings.Repeat("k", MaxKeyLength+1-len(joined)+rng.IntN(64)), "too long"
	}
}

func TestValidateKeyProperties(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for range propertyIterations {
		segments := randomKeySegments(rng)
		valid := strings.Join(segments, "/")
		if err := ValidateKey(valid); err != nil {
			t.Fatalf("ValidateKey(%q) = %v, want nil for a key built from the valid alphabet", valid, err)
		}

		invalid, rule := forbidKey(rng, segments)
		if err := ValidateKey(invalid); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("ValidateKey(%q) = %v, want ErrInvalidKey after adding a %s", invalid, err, rule)
		}
	}
}

// randomBucketName returns a valid bucket name. It holds at least one
// letter, so it is never formatted like an IPv4 address.
func randomBucketName(rng *rand.Rand) string {
	length := 3 + rng.IntN(61)
	b := make([]byte, length)
	for i := range b {
		switch {
		case i == 0 || i == length-1:
			b[i] = pick(rng, lowerAlphanumeric)
		case b[i-1] == '.':
			b[i] = pick(rng, lowerAlphanumeric+"-")
		default:
			b[i] = pick(rng, lowerAlphanumeric+"-.")
		}
	}
	b[rng.IntN(length)] = pick(rng, "abcdefghijklmnopqrstuvwxyz")
	return string(b)
}

// forbidBucketName breaks one documented ValidateBucketName rule in a valid
// name, and names the rule it broke.
func forbidBucketName(rng *rand.Rand, name string) (invalid, rule string) {
	b := []byte(name)
	switch rng.IntN(7) {
	case 0:
		b[rng.IntN(len(b))] = pick(rng, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
		return string(b), "uppercase letter"
	case 1:
		b[rng.IntN(len(b))] = pick(rng, "_ /\\:@!*\x00\x7f\xe9")
		return string(b), "disallowed character"
	case 2:
		at := 1 + rng.IntN(len(b)-1)
		return name[:at] + ".." + name[at:], "consecutive dots"
	case 3:
		if rng.IntN(2) == 0 {
			b[0] = pick(rng, "-.")
		} else {
			b[len(b)-1] = pick(rng, "-.")
		}
		return string(b), "hyphen or dot at an edge"
	case 4:
		return name[:rng.IntN(3)], "too short"
	case 5:
		return name + strings.Repeat("a", 64-len(name)+rng.IntN(16)), "too long"
	default:
		parts := make([]string, 4)
		for i := range parts {
			digits := make([]byte, 1+rng.IntN(3))
			for j := range digits {
				digits[j] = pick(rng, "0123456789")
			}
			parts[i] = string(digits)
		}
		return strings.Join(parts, "."), "IPv4 address format"
	}
}

func TestValidateBucketNameProperties(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for range propertyIterations {
		name := randomBucketName(rng)
		if err := ValidateBucketName(name); err != nil {
			t.Fatalf("ValidateBucketName(%q) = %v, want nil for a name built from the valid alphabet", name, err)
		}

		invalid, rule := forbidBucketName(rng, name)
		if err := ValidateBucketName(invalid); !errors.Is(err, ErrInvalidBucketName) {
			t.Fatalf("ValidateBucketName(%q) = %v, want ErrInvalidBucketName after adding a %s", invalid, err, rule)
		}
	}
}

// randomMetadata returns valid metadata with mixed-case keys whose
// lowercase forms are distinct, and printable ASCII values.
func randomMetadata(rng *rand.Rand) map[string]string {
	m := make(map[string]string)
	lowered := make(map[string]bool)
	for range 1 + rng.IntN(6) {
		key := make([]byte, 1+rng.IntN(16))
		for i := range key {
			key[i] = pick(rng, metadataKeyChars)
		}
		if lowered[strings.ToLower(string(key))] {
			continue
		}
		lowered[strings.ToLower(string(key))] = true
		value := make([]byte, rng.IntN(32))
		for i := range value {
			value[i] = byte(0x20 + rng.IntN(0x7f-0x20))
		}
		m[string(key)] = string(value)
	}
	return m
}

// swapCase returns key with the case of one letter flipped, or "" when key
// has no letters.
func swapCase(rng *rand.Rand, key string) string {
	var letters []int
	for i := range len(key) {
		if c := key[i]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			letters = append(letters, i)
		}
	}
	if len(letters) == 0 {
		return ""
	}
	b := []byte(key)
	b[letters[rng.IntN(len(letters))]] ^= 'a' - 'A'
	return string(b)
}

// forbidMetadata breaks one documented WithMetadata rule in valid metadata,
// and names the rule it broke.
func forbidMetadata(rng *rand.Rand, m map[string]string) (invalid map[string]string, rule string) {
	invalid = maps.Clone(m)
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	key := keys[rng.IntN(len(keys))]
	switch rng.IntN(4) {
	case 0:
		if variant := swapCase(rng, key); variant != "" {
			invalid[variant] = "case variant"
			return invalid, "key differing only in case"
		}
		invalid[""] = "empty"
		return invalid, "empty key"
	case 1:
		bad := string([]byte{pick(rng, "_ .:/\x00\x7f\xe9")})
		delete(invalid, key)
		invalid[insertAtRuneBoundary(rng, key, bad)] = m[key]
		return invalid, "disallowed key character"
	default:
		bad := string([]byte{pick(rng, "\x00\t\n\r\x1f\x7f\x80\xe9\xff")})
		invalid[key] = insertAtRuneBoundary(rng, m[key], bad)
		return invalid, "non-printable value byte"
	}
}

func TestResolveMetadataProperties(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for range propertyIterations {
		m := randomMetadata(rng)
		o, err := ResolvePutOptions(WithMetadata(m))
		if err != nil {
			t.Fatalf("ResolvePutOptions(WithMetadata(%q)) error = %v, want nil", m, err)
		}
		checkNormalizedMetadata(t, m, o.Metadata)

		invalid, rule := forbidMetadata(rng, m)
		if _, err := ResolvePutOptions(WithMetadata(invalid)); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("ResolvePutOptions(WithMetadata(%q)) error = %v, want ErrInvalidOption after adding a %s", invalid, err, rule)
		}
		if _, err := ResolveCopyOptions(WithMetadata(invalid)); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("ResolveCopyOptions(WithMetadata(%q)) error = %v, want ErrInvalidOption after adding a %s", invalid, err, rule)
		}
	}
}
