package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAuditListNeverReturnsNilItems(t *testing.T) {
	doc := NewAuditList(nil)

	assert.Equal(t, SchemaVersion, doc.SchemaVersion)
	assert.Equal(t, KindAuditList, doc.Kind)
	require.NotNil(t, doc.Items)
	assert.Empty(t, doc.Items)
	assert.Nil(t, doc.Next)
}

func TestAuditRecordJSON(t *testing.T) {
	record := AuditRecord{
		At:        "2026-10-23T10:00:00.123456789Z",
		RequestID: "req_0123456789ab",
		Actor:     AuditActor{Type: "token", ID: "tok_deadbeef", Name: "ci"},
		Action:    "service.update",
		Resource:  AuditResource{Kind: "Service", ID: "svc-1", Name: "web"},
		Result:    "ok",
		SourceIP:  "10.0.0.5",
	}

	data, err := json.Marshal(record)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"at": "2026-10-23T10:00:00.123456789Z",
		"requestId": "req_0123456789ab",
		"actor": {"type": "token", "id": "tok_deadbeef", "name": "ci"},
		"action": "service.update",
		"resource": {"kind": "Service", "id": "svc-1", "name": "web"},
		"result": "ok",
		"sourceIp": "10.0.0.5"
	}`, string(data))

	// The local actor has no ID and omits it rather than sending "".
	local := AuditActor{Type: "local"}
	data, err = json.Marshal(local)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type": "local"}`, string(data))
}

func TestAuditListJSON(t *testing.T) {
	doc := NewAuditList([]AuditRecord{{
		At:        "2026-10-23T10:00:00Z",
		RequestID: "req_0123456789ab",
		Actor:     AuditActor{Type: "local"},
		Action:    "service.create",
		Resource:  AuditResource{Kind: "Service"},
		Result:    "ok",
	}})

	data, err := json.Marshal(doc)
	require.NoError(t, err)

	var decoded struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		Items         []struct {
			Action string `json:"action"`
			Actor  struct {
				Type string `json:"type"`
			} `json:"actor"`
		} `json:"items"`
		Next *string `json:"next"`
	}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, SchemaVersion, decoded.SchemaVersion)
	assert.Equal(t, KindAuditList, decoded.Kind)
	require.Len(t, decoded.Items, 1)
	assert.Equal(t, "service.create", decoded.Items[0].Action)
	assert.Equal(t, "local", decoded.Items[0].Actor.Type)
	assert.Nil(t, decoded.Next)
}
