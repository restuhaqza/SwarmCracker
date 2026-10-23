// This file owns the phase-2 service write routes: create, patch, delete and
// rollback.
//
// The HTTP layer only owns transport concerns: the request media type, the body
// cap, the strict decode, the presence of If-Match and the status code a typed
// failure maps to. Reference resolution, validation of the spec, the merge-patch
// overlay and the revision comparison all live in pkg/client, the only place
// that can see Meta.Version.Index. There is deliberately no second SwarmKit
// mapping here.
package apihttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// maxWriteBodyBytes bounds one write body. A service spec is kilobytes, so
	// 8 MiB is generous; the cap exists so a bad client cannot exhaust the
	// manager's memory.
	maxWriteBodyBytes = 8 << 20
)

// createService serves POST /v1/services.
func (s *Server) createService(w http.ResponseWriter, r *http.Request) {
	if status, err := requireContentType(r, MediaType); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ifNoneMatch := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if ifNoneMatch != "" && ifNoneMatch != "*" {
		s.writeError(w, r, invalidField("If-None-Match", `must be exactly "*" on create`))
		return
	}

	var body v1.ServiceWrite
	if status, err := readJSONWrite(w, r, &body); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	service, err := s.cfg.Client.CreateService(r.Context(), body,
		v1.WriteOptions{IfNoneMatch: ifNoneMatch})
	if err != nil {
		s.recordServiceAudit(r, "service.create",
			v1.AuditResource{Kind: v1.KindService, Name: body.Name}, "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.recordServiceAudit(r, "service.create", serviceAuditResource(service, body.Name), "ok")
	s.writeJSONWithETag(w, r, http.StatusCreated,
		v1.NewServiceDocument(service), revisionETag(service.Revision))
}

// updateService serves PATCH /v1/services/{id}. The body is an RFC 7386 merge
// patch; pkg/client parses and applies it over the current spec.
func (s *Server) updateService(w http.ResponseWriter, r *http.Request) {
	if status, err := requireContentType(r, v1.MergePatchMediaType); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch == "" {
		// 428 before the body is even read: the caller cannot have a valid
		// precondition to evaluate, and the contract names the missing header.
		s.writeWriteError(w, r, output.PreconditionRequired())
		return
	}

	patch, status, err := readWriteBody(w, r)
	if err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ref := r.PathValue("id")
	service, err := s.cfg.Client.UpdateService(r.Context(), ref, patch,
		v1.WriteOptions{IfMatch: ifMatch})
	if err != nil {
		s.recordServiceAudit(r, "service.update",
			v1.AuditResource{Kind: v1.KindService, Name: ref}, "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.recordServiceAudit(r, "service.update", serviceAuditResource(service, ref), "ok")
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewServiceDocument(service), revisionETag(service.Revision))
}

// deleteService serves DELETE /v1/services/{id}.
func (s *Server) deleteService(w http.ResponseWriter, r *http.Request) {
	ignoreMissing, status, err := parseIgnoreMissing(r.URL.Query())
	if err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	ref := r.PathValue("id")
	service, err := s.cfg.Client.DeleteService(r.Context(), ref, v1.WriteOptions{
		IfMatch:       strings.TrimSpace(r.Header.Get("If-Match")),
		IgnoreMissing: ignoreMissing,
	})
	if err != nil {
		s.recordServiceAudit(r, "service.delete",
			v1.AuditResource{Kind: v1.KindService, Name: ref}, "error")
		s.writeWriteError(w, r, err)
		return
	}

	// A miss with ignoreMissing is a zero Service: the audit record still names
	// the reference that was asked for.
	s.recordServiceAudit(r, "service.delete", serviceAuditResource(service, ref), "ok")
	w.WriteHeader(http.StatusNoContent)
}

// rollbackService serves POST /v1/services/{id}/rollback.
func (s *Server) rollbackService(w http.ResponseWriter, r *http.Request) {
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch == "" {
		s.writeWriteError(w, r, output.PreconditionRequired())
		return
	}

	// A rollback takes no body. An empty body (or whitespace) is fine; anything
	// else is a client mistake, not something to silently ignore.
	body, status, err := readWriteBody(w, r)
	if err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		s.writeError(w, r, output.NewError(output.CodeInvalid,
			"a rollback takes no request body").
			WithDetails(map[string]any{"fields": map[string]any{"body": "must be empty"}}))
		return
	}

	ref := r.PathValue("id")
	service, err := s.cfg.Client.RollbackService(r.Context(), ref,
		v1.WriteOptions{IfMatch: ifMatch})
	if err != nil {
		s.recordServiceAudit(r, "service.rollback",
			v1.AuditResource{Kind: v1.KindService, Name: ref}, "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.recordServiceAudit(r, "service.rollback", serviceAuditResource(service, ref), "ok")
	s.writeJSONWithETag(w, r, http.StatusOK,
		v1.NewServiceDocument(service), revisionETag(service.Revision))
}

// requireContentType enforces the request media type. It returns the status and
// typed error to write, or (0, nil).
func requireContentType(r *http.Request, want string) (int, error) {
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return http.StatusUnsupportedMediaType, unsupportedMediaType(want)
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil || !strings.EqualFold(mediaType, want) {
		return http.StatusUnsupportedMediaType, unsupportedMediaType(want)
	}
	return 0, nil
}

func unsupportedMediaType(want string) error {
	return output.NewErrorf(output.CodeInvalid,
		"this route requires Content-Type: %s", want).
		WithDetails(map[string]any{
			"reason":   "unsupported_media_type",
			"expected": want,
		})
}

// readJSONWrite decodes one strict JSON body under the write cap. It returns the
// status to use on failure (400 or 413) and no error on success.
func readJSONWrite(w http.ResponseWriter, r *http.Request, out any) (int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBodyBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(out); err != nil {
		return decodeWriteFailure(err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		// A body longer than the cap can trip the limiter while looking for the
		// trailing token; report the size, not a shape error.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return decodeWriteFailure(err)
		}
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"the request body must contain exactly one JSON object").
			WithDetails(map[string]any{"reason": "invalid_body"})
	}
	return 0, nil
}

// readWriteBody reads a raw write body under the same cap.
func readWriteBody(w http.ResponseWriter, r *http.Request) ([]byte, int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		status, mapped := decodeWriteFailure(err)
		return nil, status, mapped
	}
	return body, 0, nil
}

// decodeWriteFailure maps a decode or read failure onto its status and typed
// error. 413 is the only status the taxonomy cannot infer from the code.
func decodeWriteFailure(err error) (int, error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge, output.NewErrorf(output.CodeInvalid,
			"the request body exceeds %d bytes", maxWriteBodyBytes).
			WithDetails(map[string]any{
				"reason": "payload_too_large",
				"limit":  maxWriteBodyBytes,
			})
	}
	if errors.Is(err, io.EOF) {
		return http.StatusBadRequest, output.NewError(output.CodeInvalid, "the request body is empty")
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return http.StatusBadRequest, invalidField(typeErr.Field, "has the wrong type")
	}

	const unknownFieldPrefix = "json: unknown field "
	if strings.HasPrefix(err.Error(), unknownFieldPrefix) {
		field := strings.Trim(strings.TrimPrefix(err.Error(), unknownFieldPrefix), `"`)
		return http.StatusBadRequest, invalidField(field, "is not a field of this document")
	}

	return http.StatusBadRequest, output.NewErrorf(output.CodeInvalid, "invalid JSON body: %v", err).
		WithDetails(map[string]any{"reason": "invalid_body"})
}

// invalidField is the one invalid-input document: the contract names the
// offending field in details.fields.
func invalidField(path, reason string) error {
	return output.NewErrorf(output.CodeInvalid, "%s %s", path, reason).
		WithDetails(map[string]any{"fields": map[string]any{path: reason}})
}

// parseIgnoreMissing reads the delete-only ignoreMissing query parameter.
func parseIgnoreMissing(query url.Values) (bool, int, error) {
	if !query.Has("ignoreMissing") {
		return false, 0, nil
	}
	raw := query.Get("ignoreMissing")
	ignore, err := strconv.ParseBool(raw)
	if err != nil {
		return false, http.StatusBadRequest, invalidField("ignoreMissing", "must be true or false")
	}
	return ignore, 0, nil
}

// writeWriteError maps a typed write failure onto its HTTP status.
//
// 428 and 412 are HTTP statuses for the same stable `conflict` code: the reason
// in details is what distinguishes "you sent no If-Match", "your revision is
// stale" and "the name already exists". Everything else uses the shared mapping.
func (s *Server) writeWriteError(w http.ResponseWriter, r *http.Request, err error) {
	typed := typedError(err)
	status := statusForCode(typed.Code)

	if typed.Code == output.CodeConflict {
		switch typed.Details["reason"] {
		case output.ReasonPreconditionRequired:
			status = http.StatusPreconditionRequired
		case output.ReasonPreconditionFailed:
			status = http.StatusPreconditionFailed
		}
	}

	s.writeErrorStatus(w, r, status, typed)
}

// serviceAuditResource names the service a write touched. A failed create never
// produced an ID, and a delete miss has no document: the reference the caller
// used is the honest name.
func serviceAuditResource(service v1.Service, fallbackName string) v1.AuditResource {
	resource := v1.AuditResource{
		Kind: v1.KindService,
		ID:   service.ID,
		Name: service.Name,
	}
	if resource.ID == "" && resource.Name == "" {
		resource.Name = fallbackName
	}
	return resource
}

// recordServiceAudit appends one audit record for a service mutation.
//
// The mutation has already committed by the time this runs, so a failed append
// must not turn a successful write into an error response: the caller would
// retry a create that already happened. The store is append-only and fsyncs
// each line; a failure here is an operator problem to surface from the log, not
// a reason to lie about the write.
func (s *Server) recordServiceAudit(r *http.Request, action string, resource v1.AuditResource, result string) {
	_ = s.recordAudit(r, action, resource, result)
}

// revisionETag renders a SwarmKit revision as a strong ETag. The revision is the
// contract's opaque token; an empty one falls back to the body hash in
// writeJSONWithETag.
func revisionETag(revision string) string {
	if revision == "" {
		return ""
	}
	return `"` + revision + `"`
}
