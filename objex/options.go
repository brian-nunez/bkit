package objex

import (
	"fmt"
	"maps"
	"strings"
)

// PutOption configures Bucket.Put and Presigner.PresignPut.
type PutOption interface{ applyPut(*PutOptions) }

// GetOption configures Bucket.Get.
type GetOption interface{ applyGet(*GetOptions) }

// ListOption configures Bucket.List.
type ListOption interface{ applyList(*ListOptions) }

// CopyOption configures Bucket.Copy.
type CopyOption interface{ applyCopy(*CopyOptions) }

// PutOptions is the resolved form of PutOption values. Drivers obtain it from
// ResolvePutOptions; applications use the With* and If* functions instead.
type PutOptions struct {
	// ContentType is the media type to store; empty lets the driver choose.
	ContentType string

	// Metadata is user metadata to store, with lowercase keys.
	Metadata map[string]string

	// Size is the number of bytes the reader will yield, or -1 if unknown.
	Size int64

	// IfMatch requires the existing object's ETag to equal this value.
	IfMatch string

	// IfNotExists requires that no object exists under the key.
	IfNotExists bool
}

// GetOptions is the resolved form of GetOption values.
type GetOptions struct {
	// Range limits the read to part of the object; nil reads everything.
	Range *Range

	// IfMatch requires the object's ETag to equal this value.
	IfMatch string
}

// ListOptions is the resolved form of ListOption values.
type ListOptions struct {
	// Prefix limits results to keys starting with it.
	Prefix string

	// Delimiter groups keys sharing the text between Prefix and the first
	// occurrence of Delimiter into one entry with IsPrefix set.
	Delimiter string

	// StartAfter limits results to keys sorting after it.
	StartAfter string
}

// CopyOptions is the resolved form of CopyOption values.
type CopyOptions struct {
	// ContentType replaces the content type when non-empty.
	ContentType string

	// Metadata replaces all user metadata when ReplaceMetadata is true.
	Metadata map[string]string

	// ReplaceMetadata reports whether WithMetadata was given.
	ReplaceMetadata bool

	// IfMatch requires the source object's ETag to equal this value.
	IfMatch string
}

// Range selects part of an object.
type Range struct {
	// Offset is the first byte to read, counted from zero.
	Offset int64

	// Length is the number of bytes to read; a negative Length reads to the
	// end of the object. Reads past the end are truncated.
	Length int64
}

// ResolvePutOptions applies opts in order and validates the result. Errors
// match ErrInvalidOption.
func ResolvePutOptions(opts ...PutOption) (PutOptions, error) {
	o := PutOptions{Size: -1}
	for _, opt := range opts {
		if opt != nil {
			opt.applyPut(&o)
		}
	}
	if o.Size < -1 {
		return PutOptions{}, invalidOption("size must be -1 (unknown) or at least 0")
	}
	if o.IfMatch != "" && o.IfNotExists {
		return PutOptions{}, invalidOption("IfMatch and IfNotExists cannot be combined")
	}
	metadata, err := normalizeMetadata(o.Metadata)
	if err != nil {
		return PutOptions{}, err
	}
	o.Metadata = metadata
	return o, nil
}

// ResolveGetOptions applies opts in order and validates the result. A
// malformed range returns an error matching ErrInvalidRange.
func ResolveGetOptions(opts ...GetOption) (GetOptions, error) {
	var o GetOptions
	for _, opt := range opts {
		if opt != nil {
			opt.applyGet(&o)
		}
	}
	if o.Range != nil {
		if o.Range.Offset < 0 {
			return GetOptions{}, fmt.Errorf("%w: offset must not be negative", ErrInvalidRange)
		}
		if o.Range.Length == 0 {
			return GetOptions{}, fmt.Errorf("%w: length must not be zero", ErrInvalidRange)
		}
	}
	return o, nil
}

// ResolveListOptions applies opts in order.
func ResolveListOptions(opts ...ListOption) ListOptions {
	var o ListOptions
	for _, opt := range opts {
		if opt != nil {
			opt.applyList(&o)
		}
	}
	return o
}

// ResolveCopyOptions applies opts in order and validates the result. Errors
// match ErrInvalidOption.
func ResolveCopyOptions(opts ...CopyOption) (CopyOptions, error) {
	var o CopyOptions
	for _, opt := range opts {
		if opt != nil {
			opt.applyCopy(&o)
		}
	}
	metadata, err := normalizeMetadata(o.Metadata)
	if err != nil {
		return CopyOptions{}, err
	}
	o.Metadata = metadata
	return o, nil
}

// normalizeMetadata copies m with lowercase keys and validates it: keys are
// ASCII letters, digits, and hyphens; values are printable ASCII. These are
// the characters every backend stores unchanged.
func normalizeMetadata(m map[string]string) (map[string]string, error) {
	if m == nil {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for key, value := range m {
		if key == "" {
			return nil, invalidOption("metadata keys must not be empty")
		}
		for i := range len(key) {
			if c := key[i]; !isMetadataKeyChar(c) {
				return nil, invalidOption(fmt.Sprintf("metadata key %q may contain only letters, digits, and hyphens", key))
			}
		}
		for i := range len(value) {
			if value[i] < 0x20 || value[i] > 0x7e {
				return nil, invalidOption(fmt.Sprintf("metadata value for %q must be printable ASCII", key))
			}
		}
		lower := strings.ToLower(key)
		if _, duplicate := out[lower]; duplicate {
			return nil, invalidOption(fmt.Sprintf("metadata key %q differs from another key only in case", key))
		}
		out[lower] = value
	}
	return out, nil
}

func isMetadataKeyChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-'
}

func invalidOption(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidOption, reason)
}

// ContentTypeOption sets the content type of a Put or Copy.
type ContentTypeOption struct{ contentType string }

// WithContentType sets the object's media type. On Copy it replaces the
// source's content type.
func WithContentType(contentType string) ContentTypeOption {
	return ContentTypeOption{contentType: contentType}
}

func (o ContentTypeOption) applyPut(p *PutOptions)   { p.ContentType = o.contentType }
func (o ContentTypeOption) applyCopy(c *CopyOptions) { c.ContentType = o.contentType }

// MetadataOption sets the user metadata of a Put or Copy.
type MetadataOption struct{ metadata map[string]string }

// WithMetadata sets user metadata. Keys are stored in lowercase and may
// contain only ASCII letters, digits, and hyphens; values must be printable
// ASCII. On Copy it replaces all of the source's metadata. Drivers that
// cannot store metadata return an error matching ErrUnsupported.
func WithMetadata(metadata map[string]string) MetadataOption {
	return MetadataOption{metadata: maps.Clone(metadata)}
}

func (o MetadataOption) applyPut(p *PutOptions) { p.Metadata = maps.Clone(o.metadata) }

func (o MetadataOption) applyCopy(c *CopyOptions) {
	c.Metadata = maps.Clone(o.metadata)
	c.ReplaceMetadata = true
}

// SizeOption declares the size of a Put.
type SizeOption struct{ size int64 }

// WithSize declares how many bytes the reader passed to Put will yield.
// Drivers may use it to avoid multipart uploads; it is never required.
func WithSize(size int64) SizeOption { return SizeOption{size: size} }

func (o SizeOption) applyPut(p *PutOptions) { p.Size = o.size }

// IfMatchOption makes an operation conditional on an object's ETag.
type IfMatchOption struct{ etag string }

// IfMatch makes the operation succeed only if the object's current ETag
// equals etag; otherwise it returns an error matching ErrPreconditionFailed.
// On Put it guards the object being replaced, on Get the object being read,
// and on Copy the source object. A missing object returns an error matching
// ErrNotFound.
func IfMatch(etag string) IfMatchOption { return IfMatchOption{etag: strings.Trim(etag, `"`)} }

func (o IfMatchOption) applyPut(p *PutOptions)   { p.IfMatch = o.etag }
func (o IfMatchOption) applyGet(g *GetOptions)   { g.IfMatch = o.etag }
func (o IfMatchOption) applyCopy(c *CopyOptions) { c.IfMatch = o.etag }

// IfNotExistsOption makes a Put create-only.
type IfNotExistsOption struct{}

// IfNotExists makes Put succeed only if no object exists under the key;
// otherwise it returns an error matching ErrPreconditionFailed.
func IfNotExists() IfNotExistsOption { return IfNotExistsOption{} }

func (IfNotExistsOption) applyPut(p *PutOptions) { p.IfNotExists = true }

// RangeOption limits a Get to part of an object.
type RangeOption struct{ r Range }

// WithRange reads length bytes starting at offset. A negative length reads
// to the end of the object. An offset at or beyond the end of the object
// returns an error matching ErrInvalidRange.
func WithRange(offset, length int64) RangeOption {
	return RangeOption{r: Range{Offset: offset, Length: length}}
}

func (o RangeOption) applyGet(g *GetOptions) {
	r := o.r
	g.Range = &r
}

// PrefixOption limits a List to keys with a prefix.
type PrefixOption struct{ prefix string }

// WithPrefix lists only keys starting with prefix.
func WithPrefix(prefix string) PrefixOption { return PrefixOption{prefix: prefix} }

func (o PrefixOption) applyList(l *ListOptions) { l.Prefix = o.prefix }

// DelimiterOption groups List results by a delimiter.
type DelimiterOption struct{ delimiter string }

// WithDelimiter groups keys that share the text between the prefix and the
// first occurrence of delimiter into a single entry with IsPrefix set, the
// way directories group files. Use "/" for directory-style listings.
func WithDelimiter(delimiter string) DelimiterOption {
	return DelimiterOption{delimiter: delimiter}
}

func (o DelimiterOption) applyList(l *ListOptions) { l.Delimiter = o.delimiter }

// StartAfterOption resumes a List after a key.
type StartAfterOption struct{ key string }

// WithStartAfter lists only keys that sort after key.
func WithStartAfter(key string) StartAfterOption { return StartAfterOption{key: key} }

func (o StartAfterOption) applyList(l *ListOptions) { l.StartAfter = o.key }
