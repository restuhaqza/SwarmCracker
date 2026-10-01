package golden

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArtifactMaterialize_CopiesTemplate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "template.ext4")
	require.NoError(t, os.WriteFile(src, []byte("template-contents"), 0o644))

	dst := filepath.Join(t.TempDir(), "nested", "task.ext4")
	art := &Artifact{Ref: "ubuntu@1.0.0", Path: src}

	require.NoError(t, art.Materialize(context.Background(), dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "template-contents", string(got))

	// The template must survive materialization.
	_, err = os.Stat(src)
	require.NoError(t, err)
}

func TestArtifactMaterialize_Idempotent(t *testing.T) {
	src := filepath.Join(t.TempDir(), "template.ext4")
	require.NoError(t, os.WriteFile(src, []byte("template"), 0o644))

	dst := filepath.Join(t.TempDir(), "task.ext4")
	require.NoError(t, os.WriteFile(dst, []byte("already-there"), 0o644))

	art := &Artifact{Path: src}
	require.NoError(t, art.Materialize(context.Background(), dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "already-there", string(got), "an existing destination must be left alone")
}

func TestArtifactMaterialize_MissingTemplateFails(t *testing.T) {
	art := &Artifact{Path: filepath.Join(t.TempDir(), "nope.ext4")}
	err := art.Materialize(context.Background(), filepath.Join(t.TempDir(), "task.ext4"))
	require.Error(t, err)
}

func TestArtifactMaterialize_NilAndEmpty(t *testing.T) {
	var art *Artifact
	require.Error(t, art.Materialize(context.Background(), filepath.Join(t.TempDir(), "x")))

	require.Error(t, (&Artifact{Path: "x"}).Materialize(context.Background(), ""))
}

func TestArtifactMaterialize_MkdirFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "template.ext4")
	require.NoError(t, os.WriteFile(src, []byte("t"), 0o644))

	// A regular file where a directory is needed makes MkdirAll fail.
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	art := &Artifact{Path: src}
	err := art.Materialize(context.Background(), filepath.Join(blocker, "sub", "task.ext4"))
	require.Error(t, err)
}

func TestArtifactMaterialize_FallsBackWhenCpFails(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "template.ext4")
	require.NoError(t, os.WriteFile(src, []byte("template"), 0o644))

	// A failing `cp` on PATH forces the pure-Go fallback.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "cp"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	t.Setenv("PATH", bin)

	dst := filepath.Join(t.TempDir(), "task.ext4")
	art := &Artifact{Path: src}
	require.NoError(t, art.Materialize(context.Background(), dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "template", string(got))
}

func TestCopyFileSparse_Success(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(src, []byte("hello"), 0o644))

	require.NoError(t, copyFileSparse(src, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestCopyFileSparse_MissingSource(t *testing.T) {
	dir := t.TempDir()
	err := copyFileSparse(filepath.Join(dir, "nope"), filepath.Join(dir, "dst"))
	require.Error(t, err)
}

func TestCopyFileSparse_DestinationIsDirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0o644))

	dst := filepath.Join(dir, "adir")
	require.NoError(t, os.Mkdir(dst, 0o755))

	require.Error(t, copyFileSparse(src, dst))
}
