package golden

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Extractor materialises a recipe source into destDir.
type Extractor interface {
	Extract(ctx context.Context, source Source, destDir string) error
}

// Runner executes a shell script inside a provisioned root filesystem.
type Runner interface {
	Run(ctx context.Context, rootfsDir, script string) error
}

// Ext4Builder packs a directory into an ext4 filesystem image.
type Ext4Builder interface {
	Build(ctx context.Context, sourceDir, outputPath string, minSizeBytes int64) error
}

// BuilderOptions configures a Builder. Only OutputDir is required; the
// interfaces default to the real implementations when nil.
type BuilderOptions struct {
	WorkDir   string
	OutputDir string
	Force     bool
	Extractor Extractor
	Runner    Runner
	Ext4      Ext4Builder
	Now       func() time.Time
}

// Builder turns recipes into sealed artifacts.
type Builder struct {
	opts BuilderOptions
}

// NewBuilder validates options and fills in defaults.
func NewBuilder(opts BuilderOptions) (*Builder, error) {
	if opts.OutputDir == "" {
		return nil, fmt.Errorf("golden: OutputDir is required")
	}
	if opts.WorkDir == "" {
		opts.WorkDir = os.TempDir()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Extractor == nil {
		opts.Extractor = NewOCIExtractor(nil)
	}
	if opts.Runner == nil {
		opts.Runner = NewChrootRunner()
	}
	if opts.Ext4 == nil {
		opts.Ext4 = NewExt4Creator()
	}
	return &Builder{opts: opts}, nil
}

// Result describes a completed (or reused) build.
type Result struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Digest       string `json:"digest"`
	ArtifactPath string `json:"artifact"`
	MetadataPath string `json:"metadata"`
	SizeBytes    int64  `json:"sizeBytes"`
	SHA256       string `json:"sha256"`
	Skipped      bool   `json:"skipped"`
}

// ArtifactMetadata is the sidecar written next to every artifact.
type ArtifactMetadata struct {
	APIVersion       string    `json:"apiVersion"`
	Kind             string    `json:"kind"`
	Name             string    `json:"name"`
	Version          string    `json:"version"`
	RecipeDigest     string    `json:"recipeDigest"`
	Arch             []string  `json:"arch"`
	Source           Source    `json:"source"`
	Init             Init      `json:"init"`
	KernelProfile    string    `json:"kernelProfile"`
	Runtime          Runtime   `json:"runtime"`
	RootMinSizeBytes int64     `json:"rootMinSizeBytes"`
	Artifact         string    `json:"artifact"`
	SizeBytes        int64     `json:"sizeBytes"`
	SHA256           string    `json:"sha256"`
	BuiltAt          time.Time `json:"builtAt"`
}

// Build produces the artifact for r. When an up-to-date artifact already exists
// and Force is false, it is reused without touching the network or the disk.
func (b *Builder) Build(ctx context.Context, r *Recipe) (*Result, error) {
	if r == nil {
		return nil, fmt.Errorf("golden: recipe is nil")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.Spec.Build != BuildGolden {
		return nil, fmt.Errorf(
			"golden: recipe %q uses build=%q and is assembled by the task-time image preparer, not `image build`",
			r.Metadata.Name, r.Spec.Build)
	}

	digest := r.Digest()
	base := ArtifactBaseName(r.Metadata.Name, r.Metadata.Version)
	if err := os.MkdirAll(b.opts.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("golden: create output dir: %w", err)
	}
	artifactPath := filepath.Join(b.opts.OutputDir, base+".ext4")
	metadataPath := filepath.Join(b.opts.OutputDir, base+".json")

	if !b.opts.Force {
		if res, ok := b.reuse(artifactPath, metadataPath, digest); ok {
			log.Info().
				Str("artifact", artifactPath).
				Msg("Golden image is up to date, reusing")
			return res, nil
		}
	}

	log.Info().
		Str("name", r.Metadata.Name).
		Str("version", r.Metadata.Version).
		Str("source", r.Spec.Source.Ref).
		Msg("Building golden image")

	workDir, err := os.MkdirTemp(b.opts.WorkDir, "swarmcracker-golden-")
	if err != nil {
		return nil, fmt.Errorf("golden: create work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	rootfsDir := filepath.Join(workDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		return nil, fmt.Errorf("golden: create rootfs dir: %w", err)
	}

	if err := b.opts.Extractor.Extract(ctx, r.Spec.Source, rootfsDir); err != nil {
		return nil, fmt.Errorf("golden: extract %s: %w", r.Spec.Source.Ref, err)
	}

	if script := strings.TrimSpace(r.Spec.Provision); script != "" {
		if err := b.opts.Runner.Run(ctx, rootfsDir, script); err != nil {
			return nil, fmt.Errorf("golden: provision %s: %w", r.Metadata.Name, err)
		}
	}
	for i, seal := range r.Spec.Seal {
		if strings.TrimSpace(seal) == "" {
			continue
		}
		if err := b.opts.Runner.Run(ctx, rootfsDir, seal); err != nil {
			return nil, fmt.Errorf("golden: seal[%d] %s: %w", i, r.Metadata.Name, err)
		}
	}

	minSize, err := r.RootMinSizeBytes()
	if err != nil {
		return nil, fmt.Errorf("golden: disk.rootMinSize: %w", err)
	}

	if err := b.opts.Ext4.Build(ctx, rootfsDir, artifactPath, minSize); err != nil {
		return nil, fmt.Errorf("golden: build rootfs for %s: %w", r.Metadata.Name, err)
	}

	info, err := os.Stat(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("golden: stat artifact: %w", err)
	}
	sum, err := fileSHA256(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("golden: hash artifact: %w", err)
	}

	md := ArtifactMetadata{
		APIVersion:       APIVersionV1Alpha1,
		Kind:             KindGoldenImage,
		Name:             r.Metadata.Name,
		Version:          r.Metadata.Version,
		RecipeDigest:     digest,
		Arch:             r.Spec.Arch,
		Source:           r.Spec.Source,
		Init:             r.Spec.Init,
		KernelProfile:    r.Spec.Kernel.Profile,
		Runtime:          r.Spec.Runtime,
		RootMinSizeBytes: minSize,
		Artifact:         filepath.Base(artifactPath),
		SizeBytes:        info.Size(),
		SHA256:           sum,
		BuiltAt:          b.opts.Now().UTC(),
	}
	if err := writeMetadataAtomic(metadataPath, md); err != nil {
		return nil, fmt.Errorf("golden: write metadata: %w", err)
	}

	log.Info().
		Str("artifact", artifactPath).
		Int64("size_bytes", info.Size()).
		Str("sha256", sum).
		Msg("Golden image built")

	return &Result{
		Name:         r.Metadata.Name,
		Version:      r.Metadata.Version,
		Digest:       digest,
		ArtifactPath: artifactPath,
		MetadataPath: metadataPath,
		SizeBytes:    info.Size(),
		SHA256:       sum,
	}, nil
}

// reuse returns a result when the artifact and metadata already match digest.
func (b *Builder) reuse(artifactPath, metadataPath, digest string) (*Result, bool) {
	info, err := os.Stat(artifactPath)
	if err != nil || info.Size() == 0 {
		return nil, false
	}
	md, err := ReadMetadata(metadataPath)
	if err != nil || md.RecipeDigest != digest {
		return nil, false
	}
	return &Result{
		Name:         md.Name,
		Version:      md.Version,
		Digest:       md.RecipeDigest,
		ArtifactPath: artifactPath,
		MetadataPath: metadataPath,
		SizeBytes:    info.Size(),
		SHA256:       md.SHA256,
		Skipped:      true,
	}, true
}

// ReadMetadata loads an artifact metadata sidecar.
func ReadMetadata(path string) (*ArtifactMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("golden: read metadata %s: %w", path, err)
	}
	var md ArtifactMetadata
	if err := json.Unmarshal(data, &md); err != nil {
		return nil, fmt.Errorf("golden: parse metadata %s: %w", path, err)
	}
	return &md, nil
}

// writeMetadataAtomic writes JSON to path via a temp file and rename.
func writeMetadataAtomic(path string, md ArtifactMetadata) error {
	data, err := json.MarshalIndent(md, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".golden-md-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// fileSHA256 returns the hex-encoded SHA-256 of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
