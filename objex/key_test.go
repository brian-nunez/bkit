package objex

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateKey(t *testing.T) {
	valid := []string{
		"a",
		"file.txt",
		"dir/sub/file.txt",
		".hidden",
		"dir/.config",
		"spaces are fine",
		"unicode/日本語.txt",
		"a..b",
		strings.Repeat("k", MaxKeyLength),
	}
	for _, key := range valid {
		if err := ValidateKey(key); err != nil {
			t.Errorf("ValidateKey(%q) = %v, want nil", key, err)
		}
	}

	invalid := []string{
		"",
		"/leading",
		"trailing/",
		"a//b",
		".",
		"..",
		"a/./b",
		"a/../b",
		"../escape",
		`back\slash`,
		"nul\x00byte",
		"new\nline",
		"del\x7f",
		"bad\xffutf8",
		strings.Repeat("k", MaxKeyLength+1),
	}
	for _, key := range invalid {
		if err := ValidateKey(key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ValidateKey(%q) = %v, want ErrInvalidKey", key, err)
		}
	}
}

func TestValidateBucketName(t *testing.T) {
	for _, name := range []string{"abc", "my-bucket", "my.bucket.v2", "123", strings.Repeat("a", 63), "1.2.3"} {
		if err := ValidateBucketName(name); err != nil {
			t.Errorf("ValidateBucketName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{
		"", "ab", strings.Repeat("a", 64), "Upper", "under_score", "space name",
		"-start", "end-", ".start", "end.", "a..b", "192.168.0.1", "10.0.0.255",
	} {
		if err := ValidateBucketName(name); !errors.Is(err, ErrInvalidBucketName) {
			t.Errorf("ValidateBucketName(%q) = %v, want ErrInvalidBucketName", name, err)
		}
	}
}
