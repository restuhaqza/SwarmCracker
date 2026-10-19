package output

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FromGRPC maps a gRPC failure to the stable CLI error taxonomy. A non-gRPC
// error is treated as a transport failure (`unreachable`), which is the right
// classification for dial, TLS and certificate-loading failures.
func FromGRPC(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewError(CodeTimeout, err.Error())
	}
	if errors.Is(err, context.Canceled) {
		return NewError(CodeTimeout, err.Error())
	}

	st, ok := status.FromError(err)
	if !ok {
		return NewError(CodeUnreachable, err.Error())
	}

	code := CodeInternal
	switch st.Code() {
	case codes.NotFound:
		code = CodeNotFound
	case codes.InvalidArgument, codes.OutOfRange:
		code = CodeInvalid
	case codes.AlreadyExists, codes.FailedPrecondition, codes.Aborted:
		code = CodeConflict
	case codes.PermissionDenied:
		code = CodeForbidden
	case codes.Unauthenticated:
		code = CodeUnauthenticated
	case codes.ResourceExhausted:
		code = CodeRateLimited
	case codes.Unavailable:
		code = CodeUnavailable
	case codes.DeadlineExceeded:
		code = CodeTimeout
	case codes.Unimplemented:
		code = CodeNotImplemented
	case codes.Canceled:
		code = CodeTimeout
	}
	return NewError(code, st.Message())
}
