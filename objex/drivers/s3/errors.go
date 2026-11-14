package s3

import (
	"context"
	"errors"
	"net/http"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/brian-nunez/bkit/objex"
)

// wrap returns err as an *objex.Error classified by its S3 error code or
// HTTP status. Context errors keep their identity through the SDK's
// wrapping, so errors.Is(err, context.Canceled) still holds.
func wrap(op, bucket, key string, err error) error {
	return objex.NewError(op, bucket, key, classify(err), err)
}

func classify(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchVersion":
			return objex.ErrNotFound
		case "NoSuchBucket":
			return objex.ErrBucketNotFound
		case "AccessDenied", "Forbidden", "InvalidAccessKeyId", "SignatureDoesNotMatch",
			"AllAccessDisabled", "ExpiredToken", "InvalidToken", "AccountProblem":
			return objex.ErrPermission
		case "PreconditionFailed", "ConditionalRequestConflict":
			return objex.ErrPreconditionFailed
		case "InvalidRange":
			return objex.ErrInvalidRange
		case "BucketAlreadyExists", "BucketAlreadyOwnedByYou":
			return objex.ErrBucketExists
		case "BucketNotEmpty":
			return objex.ErrBucketNotEmpty
		case "InvalidBucketName":
			return objex.ErrInvalidBucketName
		case "NotImplemented":
			return objex.ErrUnsupported
		}
	}

	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.HTTPStatusCode() {
		case http.StatusNotFound:
			return objex.ErrNotFound
		case http.StatusForbidden, http.StatusUnauthorized:
			return objex.ErrPermission
		case http.StatusPreconditionFailed:
			return objex.ErrPreconditionFailed
		case http.StatusRequestedRangeNotSatisfiable:
			return objex.ErrInvalidRange
		case http.StatusNotImplemented:
			return objex.ErrUnsupported
		}
	}
	return nil
}
