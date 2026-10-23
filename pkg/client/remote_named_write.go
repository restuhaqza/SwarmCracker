// This file owns the remote HTTP writes for configs, secrets, networks and
// nodes. Like remote.go, it only builds the wire request: the server resolves
// the reference, validates the body and evaluates the precondition, so this
// client never pre-resolves or duplicates the local mapping.
package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// postJSON sends a create with the v1 media type and decodes the response.
func (r *remoteClient) postJSON(ctx context.Context, segments []string, value any, headers map[string]string, out any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "encode %s: %v", strings.Join(segments, "/"), err)
	}

	resp, err := r.do(ctx, remoteRequest{
		method:      http.MethodPost,
		segments:    segments,
		body:        body,
		contentType: mediaTypeV1,
		headers:     headers,
	})
	if err != nil {
		return err
	}
	return decodeJSON(strings.Join(segments, "/"), resp.body, out)
}

// patchJSON sends a raw merge patch and decodes the response.
func (r *remoteClient) patchJSON(ctx context.Context, segments []string, patch []byte, opts v1.WriteOptions, out any) error {
	if err := requireIfMatch(opts.IfMatch); err != nil {
		return err
	}

	resp, err := r.do(ctx, remoteRequest{
		method:      http.MethodPatch,
		segments:    segments,
		body:        patch,
		contentType: v1.MergePatchMediaType,
		headers:     ifMatchHeader(opts.IfMatch),
	})
	if err != nil {
		return err
	}
	return decodeJSON(strings.Join(segments, "/"), resp.body, out)
}

// deleteRequest sends DELETE with the forwarded preconditions. The caller has
// already fetched the previous document.
func (r *remoteClient) deleteRequest(ctx context.Context, segments []string, opts v1.WriteOptions) error {
	query := url.Values{}
	if opts.IgnoreMissing {
		query.Set("ignoreMissing", "true")
	}

	_, err := r.do(ctx, remoteRequest{
		method:   http.MethodDelete,
		segments: segments,
		query:    query,
		headers:  ifMatchHeader(opts.IfMatch),
	})
	return err
}

// missingPreconditionPassed reports whether a failed GET should still go on to
// DELETE: If-Match asks the server to evaluate 412 and ignoreMissing asks it to
// answer 204, so the client must not decide either locally.
func missingPreconditionPassed(err error, opts v1.WriteOptions) bool {
	return err != nil && opts.IfMatch == "" && !opts.IgnoreMissing
}

func (r *remoteClient) createConfig(ctx context.Context, write v1.ConfigWrite, opts v1.WriteOptions) (v1.Config, error) {
	if err := checkCreateOnly(opts); err != nil {
		return v1.Config{}, err
	}
	var doc v1.ConfigDocument
	if err := r.postJSON(ctx, []string{"v1", "configs"}, write,
		ifNoneMatchHeader(opts.IfNoneMatch), &doc); err != nil {
		return v1.Config{}, err
	}
	return doc.Config, nil
}

func (r *remoteClient) updateConfig(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Config, error) {
	var doc v1.ConfigDocument
	if err := r.patchJSON(ctx, []string{"v1", "configs", ref}, patch, opts, &doc); err != nil {
		return v1.Config{}, err
	}
	return doc.Config, nil
}

func (r *remoteClient) deleteConfig(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Config, error) {
	previous, getErr := r.getConfig(ctx, ref)
	if missingPreconditionPassed(getErr, opts) {
		return v1.Config{}, getErr
	}
	if err := r.deleteRequest(ctx, []string{"v1", "configs", ref}, opts); err != nil {
		return v1.Config{}, err
	}
	return previous, nil
}

func (r *remoteClient) createSecret(ctx context.Context, write v1.SecretWrite, opts v1.WriteOptions) (v1.Secret, error) {
	if err := checkCreateOnly(opts); err != nil {
		return v1.Secret{}, err
	}
	var doc v1.SecretDocument
	if err := r.postJSON(ctx, []string{"v1", "secrets"}, write,
		ifNoneMatchHeader(opts.IfNoneMatch), &doc); err != nil {
		return v1.Secret{}, err
	}
	return doc.Secret, nil
}

func (r *remoteClient) updateSecret(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Secret, error) {
	var doc v1.SecretDocument
	if err := r.patchJSON(ctx, []string{"v1", "secrets", ref}, patch, opts, &doc); err != nil {
		return v1.Secret{}, err
	}
	return doc.Secret, nil
}

func (r *remoteClient) deleteSecret(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Secret, error) {
	previous, getErr := r.getSecret(ctx, ref)
	if missingPreconditionPassed(getErr, opts) {
		return v1.Secret{}, getErr
	}
	if err := r.deleteRequest(ctx, []string{"v1", "secrets", ref}, opts); err != nil {
		return v1.Secret{}, err
	}
	return previous, nil
}

func (r *remoteClient) createNetwork(ctx context.Context, write v1.NetworkWrite, opts v1.WriteOptions) (v1.Network, error) {
	if err := checkCreateOnly(opts); err != nil {
		return v1.Network{}, err
	}
	var doc v1.NetworkDocument
	if err := r.postJSON(ctx, []string{"v1", "networks"}, write,
		ifNoneMatchHeader(opts.IfNoneMatch), &doc); err != nil {
		return v1.Network{}, err
	}
	return doc.Network, nil
}

func (r *remoteClient) deleteNetwork(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Network, error) {
	previous, getErr := r.getNetwork(ctx, ref)
	if missingPreconditionPassed(getErr, opts) {
		return v1.Network{}, getErr
	}
	if err := r.deleteRequest(ctx, []string{"v1", "networks", ref}, opts); err != nil {
		return v1.Network{}, err
	}
	return previous, nil
}

func (r *remoteClient) updateNode(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Node, error) {
	var doc v1.NodeDocument
	if err := r.patchJSON(ctx, []string{"v1", "nodes", ref}, patch, opts, &doc); err != nil {
		return v1.Node{}, err
	}
	return doc.Node, nil
}

// ifNoneMatchHeader renders the optional If-None-Match header.
func ifNoneMatchHeader(ifNoneMatch string) map[string]string {
	if strings.TrimSpace(ifNoneMatch) == "" {
		return nil
	}
	return map[string]string{"If-None-Match": ifNoneMatch}
}
