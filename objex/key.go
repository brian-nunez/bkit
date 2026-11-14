package objex

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxKeyLength is the longest key, in bytes, that ValidateKey accepts.
const MaxKeyLength = 1024

// ValidateKey reports whether key can be stored by every driver. A valid key
// is valid UTF-8, 1 to MaxKeyLength bytes long, and made of "/"-separated
// segments that are non-empty and neither "." nor "..". It contains no
// backslash and no control characters. Errors match ErrInvalidKey.
func ValidateKey(key string) error {
	switch {
	case key == "":
		return invalidKey("empty key")
	case len(key) > MaxKeyLength:
		return invalidKey(fmt.Sprintf("key is %d bytes; the limit is %d", len(key), MaxKeyLength))
	case !utf8.ValidString(key):
		return invalidKey("key is not valid UTF-8")
	}

	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return invalidKey("key contains a control character")
		}
		if r == '\\' {
			return invalidKey("key contains a backslash")
		}
	}

	for segment := range strings.SplitSeq(key, "/") {
		switch segment {
		case "":
			return invalidKey("key has an empty segment, a leading slash, or a trailing slash")
		case ".", "..":
			return invalidKey("key has a \".\" or \"..\" segment")
		}
	}

	return nil
}

func invalidKey(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidKey, reason)
}

// ValidateBucketName reports whether name is usable as a bucket name by
// every driver: 3 to 63 characters of lowercase letters, digits, hyphens,
// and dots, starting and ending with a letter or digit, with no consecutive
// dots, and not formatted like an IPv4 address. Errors match
// ErrInvalidBucketName.
func ValidateBucketName(name string) error {
	if len(name) < 3 || len(name) > 63 {
		return invalidBucketName("bucket names are 3 to 63 characters")
	}

	for i := 0; i < len(name); i++ {
		c := name[i]
		isAlphanumeric := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !isAlphanumeric && c != '-' && c != '.' {
			return invalidBucketName("bucket names use only lowercase letters, digits, hyphens, and dots")
		}
		if (i == 0 || i == len(name)-1) && !isAlphanumeric {
			return invalidBucketName("bucket names start and end with a letter or digit")
		}
	}

	if strings.Contains(name, "..") {
		return invalidBucketName("bucket names contain no consecutive dots")
	}
	if looksLikeIPv4(name) {
		return invalidBucketName("bucket names are not formatted like IP addresses")
	}

	return nil
}

func invalidBucketName(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidBucketName, reason)
}

func looksLikeIPv4(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 3 {
			return false
		}
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return false
			}
		}
	}
	return true
}
