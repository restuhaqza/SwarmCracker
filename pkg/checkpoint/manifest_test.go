package checkpoint

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validManifest is a manifest this package could have written: two full chunks
// of 1 KiB and a pin. The size is small so a test reads the shape, not the
// chunk constant.
func validManifest() Manifest {
	first := testDigest(body(1024, 1))
	second := testDigest(body(1024, 2))
	return Manifest{
		FormatVersion: FormatVersion,
		ID:            "ckpt-0123456789abcdef",
		TotalSize:     2048,
		ChunkSize:     1024,
		Chunks:        []Digest{first, second},
		Pin:           testPin,
	}
}

func TestManifestValidate(t *testing.T) {
	require.NoError(t, validManifest().Validate(), "the fixture must be a manifest that passes")

	tests := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr string
	}{
		{
			name:    "no chunk of an empty image",
			mutate:  func(m *Manifest) { m.TotalSize, m.Chunks = 0, []Digest{} },
			wantErr: "",
		},
		{
			name:    "unknown format version",
			mutate:  func(m *Manifest) { m.FormatVersion = FormatVersion + 1 },
			wantErr: "formatVersion",
		},
		{
			name:    "no format version",
			mutate:  func(m *Manifest) { m.FormatVersion = 0 },
			wantErr: "formatVersion",
		},
		{
			name:    "empty id",
			mutate:  func(m *Manifest) { m.ID = "" },
			wantErr: "id",
		},
		{
			name:    "id with a separator",
			mutate:  func(m *Manifest) { m.ID = "../other" },
			wantErr: "path separator",
		},
		{
			name:    "id with a control character",
			mutate:  func(m *Manifest) { m.ID = "ckpt-\x00one" },
			wantErr: "control character",
		},
		{
			name:    "id past the ceiling",
			mutate:  func(m *Manifest) { m.ID = strings.Repeat("c", maxIDBytes+1) },
			wantErr: "at most",
		},
		{
			name:    "id that is one dot",
			mutate:  func(m *Manifest) { m.ID = "." },
			wantErr: "not a name",
		},
		{
			name:    "negative total size",
			mutate:  func(m *Manifest) { m.TotalSize = -1 },
			wantErr: "totalSize",
		},
		{
			name:    "total size past the ceiling",
			mutate:  func(m *Manifest) { m.TotalSize = maxMemorySize + 1 },
			wantErr: "256 GiB",
		},
		{
			name:    "no chunk size",
			mutate:  func(m *Manifest) { m.ChunkSize = 0 },
			wantErr: "chunkSize",
		},
		{
			name:    "chunk size past the ceiling",
			mutate:  func(m *Manifest) { m.ChunkSize = maxChunkSize + 1 },
			wantErr: "64 MiB",
		},
		{
			name:    "a chunk missing",
			mutate:  func(m *Manifest) { m.Chunks = m.Chunks[:1] },
			wantErr: "chunks has 1 entries",
		},
		{
			name:    "a chunk too many",
			mutate:  func(m *Manifest) { m.Chunks = append(m.Chunks, m.Chunks[0]) },
			wantErr: "chunks has 3 entries",
		},
		{
			name:    "a digest in the wrong case",
			mutate:  func(m *Manifest) { m.Chunks[1] = Digest("AB" + string(m.Chunks[1])[2:]) },
			wantErr: "chunks[1]",
		},
		{
			name:    "a digest that is not a SHA-256",
			mutate:  func(m *Manifest) { m.Chunks[0] = "abc123" },
			wantErr: "chunks[0]",
		},
		{
			name:    "no Firecracker pin",
			mutate:  func(m *Manifest) { m.Pin.Firecracker = "" },
			wantErr: "pin.firecracker",
		},
		{
			name:    "no kernel pin",
			mutate:  func(m *Manifest) { m.Pin.Kernel = "" },
			wantErr: "pin.kernel",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.mutate(&m)

			err := m.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidManifest, "every rejection belongs to one class")
			assert.Contains(t, err.Error(), tc.wantErr, "the message says which field is wrong")
		})
	}
}

func TestManifestVerifyPin(t *testing.T) {
	m := validManifest()

	t.Run("the node the checkpoint was captured on", func(t *testing.T) {
		assert.NoError(t, m.VerifyPin(m.Pin))
	})

	t.Run("another Firecracker version", func(t *testing.T) {
		err := m.VerifyPin(Pin{Firecracker: otherPin.Firecracker, Kernel: m.Pin.Kernel})
		require.ErrorIs(t, err, ErrPinMismatch)
		assert.Contains(t, err.Error(), m.Pin.Firecracker)
		assert.Contains(t, err.Error(), otherPin.Firecracker)
	})

	t.Run("another kernel on the same Firecracker", func(t *testing.T) {
		err := m.VerifyPin(Pin{Firecracker: m.Pin.Firecracker, Kernel: otherPin.Kernel})
		require.ErrorIs(t, err, ErrPinMismatch, "a version match says nothing about the kernel")
	})

	t.Run("a node that pins nothing is a caller mistake, not a mismatch", func(t *testing.T) {
		err := m.VerifyPin(Pin{})
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrPinMismatch)
	})
}

func TestManifestEncodeDecode(t *testing.T) {
	m := validManifest()
	data, err := m.encode()
	require.NoError(t, err)

	got, err := decodeManifest(bytes.NewReader(data), "test")
	require.NoError(t, err)
	assert.Equal(t, m, got, "a manifest survives the round trip")
}

func TestDecodeManifestRejects(t *testing.T) {
	tests := []struct {
		name    string
		body    io.Reader
		wantErr string
	}{
		{
			name:    "not a document",
			body:    strings.NewReader("this is not a manifest"),
			wantErr: "does not decode",
		},
		{
			name:    "a document of another version",
			body:    strings.NewReader(`{"formatVersion": 2, "id": "ckpt-x", "totalSize": 0, "chunkSize": 1024, "chunks": [], "pin": {"firecracker": "v1.15.1", "kernel": "k"}}`),
			wantErr: "formatVersion",
		},
		{
			name:    "a file too large to be a manifest",
			body:    io.LimitReader(zeros{}, int64(maxManifestBytes)+1),
			wantErr: "larger than",
		},
		{
			name:    "an image with no chunks named",
			body:    strings.NewReader(`{"formatVersion": 1, "id": "ckpt-x", "totalSize": 4096, "chunkSize": 1024, "chunks": [], "pin": {"firecracker": "v1.15.1", "kernel": "k"}}`),
			wantErr: "chunks has 0 entries",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeManifest(tc.body, "test")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidManifest)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
