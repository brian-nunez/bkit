package objex

import (
	"errors"
	"strings"
)

// Sentinel errors. Drivers return them wrapped in *Error; match them with
// errors.Is.
var (
	// ErrNotFound reports that the object does not exist.
	ErrNotFound = errors.New("objex: object not found")

	// ErrBucketNotFound reports that the bucket does not exist.
	ErrBucketNotFound = errors.New("objex: bucket not found")

	// ErrBucketExists reports that a bucket being created already exists.
	ErrBucketExists = errors.New("objex: bucket already exists")

	// ErrBucketNotEmpty reports that a bucket being deleted still holds
	// objects.
	ErrBucketNotEmpty = errors.New("objex: bucket not empty")

	// ErrPermission reports that the credentials do not allow the operation.
	ErrPermission = errors.New("objex: permission denied")

	// ErrPreconditionFailed reports that a condition such as IfMatch or
	// IfNotExists did not hold.
	ErrPreconditionFailed = errors.New("objex: precondition failed")

	// ErrInvalidKey reports a key that does not satisfy ValidateKey.
	ErrInvalidKey = errors.New("objex: invalid key")

	// ErrInvalidBucketName reports a bucket name that does not satisfy
	// ValidateBucketName.
	ErrInvalidBucketName = errors.New("objex: invalid bucket name")

	// ErrInvalidRange reports a range that is malformed or starts at or
	// beyond the end of the object.
	ErrInvalidRange = errors.New("objex: invalid range")

	// ErrInvalidOption reports an option value that is malformed or
	// conflicts with another option.
	ErrInvalidOption = errors.New("objex: invalid option")

	// ErrUnsupported reports an operation or option the driver cannot
	// provide.
	ErrUnsupported = errors.New("objex: unsupported")
)

// Error describes a failed operation. It matches its Kind and its Err with
// errors.Is and errors.As.
type Error struct {
	// Op names the operation, such as "put" or "list".
	Op string

	// Bucket is the bucket involved, if any.
	Bucket string

	// Key is the object key involved, if any.
	Key string

	// Kind is the sentinel error classifying the failure, or nil when the
	// failure fits no sentinel.
	Kind error

	// Err is the underlying driver or backend error, or nil.
	Err error
}

// NewError returns an *Error for op on bucket and key, classified by kind
// and caused by err. Either kind or err may be nil.
func NewError(op, bucket, key string, kind, err error) *Error {
	return &Error{Op: op, Bucket: bucket, Key: key, Kind: kind, Err: err}
}

// Error formats the operation, target, classification, and cause.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("objex: ")
	b.WriteString(e.Op)
	if e.Bucket != "" || e.Key != "" {
		b.WriteByte(' ')
		b.WriteString(e.Bucket)
		if e.Key != "" {
			if e.Bucket != "" {
				b.WriteByte('/')
			}
			b.WriteString(e.Key)
		}
	}
	if e.Kind != nil {
		b.WriteString(": ")
		b.WriteString(strings.TrimPrefix(e.Kind.Error(), "objex: "))
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns Kind and Err, omitting nil values.
func (e *Error) Unwrap() []error {
	errs := make([]error, 0, 2)
	if e.Kind != nil {
		errs = append(errs, e.Kind)
	}
	if e.Err != nil {
		errs = append(errs, e.Err)
	}
	return errs
}
