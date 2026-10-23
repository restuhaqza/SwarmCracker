package output

// This file owns the optimistic-concurrency failures of contract §7.1. They keep
// the ordinary `conflict` code: 428 Precondition Required and 412 Precondition
// Failed are HTTP statuses, not new error codes, and the HTTP layer picks between
// them from Details["reason"]. Keeping the constructors here means pkg/client and
// pkg/apihttp cannot drift apart on the details the contract promises.

// Precondition reasons carried in an Error's details. They are part of the wire
// contract: a client distinguishes 428 from 412 and a bare 409 by this value.
const (
	// ReasonPreconditionRequired marks a write that needs an If-Match header.
	ReasonPreconditionRequired = "precondition_required"
	// ReasonPreconditionFailed marks a stale, weak or unmatched If-Match.
	ReasonPreconditionFailed = "precondition_failed"
)

// PreconditionRequired reports a write that arrived without If-Match. The HTTP
// contract answers 428 with details.required naming the header.
func PreconditionRequired() *Error {
	return NewError(CodeConflict,
		"this request requires an If-Match header carrying the current revision").
		WithDetails(map[string]any{
			"required": "If-Match",
			"reason":   ReasonPreconditionRequired,
		})
}

// PreconditionFailed reports an If-Match that does not match the current
// revision. weak marks a W/"…" validator, which is never equivalent to a strong
// revision.
func PreconditionFailed(expectedRevision, currentRevision string) *Error {
	return NewError(CodeConflict,
		"the If-Match revision does not match the current revision").
		WithDetails(map[string]any{
			"expectedRevision": expectedRevision,
			"currentRevision":  currentRevision,
			"reason":           ReasonPreconditionFailed,
		})
}
