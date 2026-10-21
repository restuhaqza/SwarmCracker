package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExitCodes(t *testing.T) {
	cases := []struct {
		code Code
		exit int
	}{
		{CodeUsage, 2},
		{CodeInvalid, 2},
		{CodeUnsupportedVersion, 2},
		{CodeNotFound, 3},
		{CodeAmbiguous, 3},
		{CodeConflict, 4},
		{CodeUnauthenticated, 5},
		{CodeForbidden, 5},
		{CodeUnreachable, 6},
		{CodeTimeout, 6},
		{CodeRateLimited, 6},
		{CodeUnavailable, 6},
		{CodeCursorExpired, 1},
		{CodeInternal, 1},
		{CodeNotImplemented, 1},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			assert.Equal(t, tc.exit, ExitCode(NewError(tc.code, "boom")))
		})
	}
}

func TestRetryable(t *testing.T) {
	assert.True(t, NewError(CodeUnavailable, "down").Retryable)
	assert.True(t, NewError(CodeTimeout, "slow").Retryable)
	assert.True(t, NewError(CodeRateLimited, "later").Retryable)
	assert.True(t, NewError(CodeInternal, "bug").Retryable)
	assert.True(t, NewError(CodeUnreachable, "dial").Retryable)
	assert.False(t, NewError(CodeNotFound, "missing").Retryable)
	assert.False(t, NewError(CodeUsage, "bad flag").Retryable)
}

func TestWriteJSONEmptyServiceList(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteJSON(&buf, v1.NewServiceList(nil)))

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "ServiceList",
		"items": [],
		"next": null
	}`, buf.String())
}

func TestWriteErrorDocument(t *testing.T) {
	var buf bytes.Buffer
	err := NewError(CodeNotFound, "service nope not found").
		WithRequestID("req_123").
		WithDetails(map[string]any{"name": "nope"})
	require.NoError(t, WriteError(&buf, err))

	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	assert.Equal(t, v1.KindError, doc.Kind)
	assert.Equal(t, string(CodeNotFound), doc.Error.Code)
	assert.Equal(t, "req_123", doc.Error.RequestID)
	assert.Equal(t, "nope", doc.Error.Details["name"])
}

func TestWriteErrorUntypedBecomesInternal(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteError(&buf, errors.New("boom")))

	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, string(CodeInternal), doc.Error.Code)
	assert.True(t, doc.Error.Retryable)
}

func TestFromGRPC(t *testing.T) {
	cases := []struct {
		grpc codes.Code
		want Code
	}{
		{codes.NotFound, CodeNotFound},
		{codes.InvalidArgument, CodeInvalid},
		{codes.AlreadyExists, CodeConflict},
		{codes.FailedPrecondition, CodeConflict},
		{codes.PermissionDenied, CodeForbidden},
		{codes.Unauthenticated, CodeUnauthenticated},
		{codes.ResourceExhausted, CodeRateLimited},
		{codes.Unavailable, CodeUnavailable},
		{codes.DeadlineExceeded, CodeTimeout},
		{codes.Unimplemented, CodeNotImplemented},
		{codes.Internal, CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.grpc.String(), func(t *testing.T) {
			err := FromGRPC(status.Error(tc.grpc, "boom"))
			var typed *Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tc.want, typed.Code)
		})
	}

	var typed *Error
	require.ErrorAs(t, FromGRPC(errors.New("dial failed")), &typed)
	assert.Equal(t, CodeUnreachable, typed.Code)
}

func TestParseFormat(t *testing.T) {
	for _, in := range []string{"table", "json", "name", "ndjson", ""} {
		_, err := ParseFormat(in)
		assert.NoError(t, err, in)
	}
	_, err := ParseFormat("yaml")
	require.Error(t, err)
	var typed *Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, CodeUsage, typed.Code)
}
