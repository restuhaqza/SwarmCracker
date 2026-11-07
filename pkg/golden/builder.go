package golden

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/image"
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
	// Prober boots the finished artifact and negotiates the guest agent
	// handshake. It defaults to the real Firecracker prober, so every artifact
	// is proven bootable with a reachable agent before it is recorded.
	Prober BootProber
	// KernelProfiles maps a recipe kernel profile to a vmlinux path, the same
	// mapping the executor uses to boot a golden task. Nil means the built-in
	// registry.
	KernelProfiles map[string]string
	// FirecrackerBinary is the VMM whose version the template pins. Empty means
	// DefaultFirecrackerBinary on PATH. It is ignored when HostPin is set.
	FirecrackerBinary string
	// HostPin reports the host software (Firecracker version and kernel
	// identity) this build pins. It is a seam, not configuration: nil reads the
	// real Firecracker version and hashes the kernel the recipe's profile maps
	// to, which is what a build on a real host must record.
	HostPin func(ctx context.Context, kernelPath string) (checkpoint.Pin, error)
	// ResolveSource reports the digest the recipe's source currently resolves
	// to, which the manifest records beside the recipe digest. It runs only
	// when the artifact is really built, so an up-to-date artifact is still
	// reused without a registry call. Nil resolves an OCI reference through the
	// registry and records nothing for a source type that has no digest.
	ResolveSource func(ctx context.Context, source Source) (string, error)
	Now           func() time.Time
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
	if opts.Prober == nil {
		opts.Prober = NewFirecrackerProber()
	}
	if opts.KernelProfiles == nil {
		opts.KernelProfiles = config.DefaultKernelProfiles()
	}
	if opts.HostPin == nil {
		binary := opts.FirecrackerBinary
		opts.HostPin = func(ctx context.Context, kernelPath string) (checkpoint.Pin, error) {
			return NodePin(ctx, binary, kernelPath)
		}
	}
	if opts.ResolveSource == nil {
		opts.ResolveSource = resolveSourceDigest
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

// Build produces the artifact for r. When an up-to-date artifact already exists
// and Force is false, it is reused: reuse reads this host's pin and the
// manifest, and needs neither the registry nor the source.
func (b *Builder) Build(ctx context.Context, r *Recipe) (*Result, error) {
	if r == nil {
		return nil, fmt.Errorf("golden: recipe is nil")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.Spec.Build != BuildGolden {
		return nil, fmt.Errorf(
			"golden: recipe %q uses build=%q and is assembled by the task-time image preparer, not `template build`",
			r.Metadata.Name, r.Spec.Build)
	}
	// The pin is this host's, and it is read before anything is built: a recipe
	// that requires other host software must fail here, not after a template
	// that no node can resume has been packed.
	kernel, nodePin, err := b.buildPin(ctx, r)
	if err != nil {
		return nil, err
	}

	digest := r.Digest()
	base := ArtifactBaseName(r.Metadata.Name, r.Metadata.Version)
	if err := os.MkdirAll(b.opts.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("golden: create output dir: %w", err)
	}
	artifactPath := filepath.Join(b.opts.OutputDir, base+".ext4")
	metadataPath := filepath.Join(b.opts.OutputDir, base+".json")

	if !b.opts.Force {
		if res, ok := b.reuse(artifactPath, metadataPath, digest, nodePin); ok {
			log.Info().
				Str("artifact", artifactPath).
				Msg("Golden image is up to date, reusing")
			return res, nil
		}
	}

	// What the source resolved to is an observation of this build, so it is
	// made only when the artifact is really being built: an up-to-date artifact
	// is still reused without a registry call.
	baseDigest, err := b.opts.ResolveSource(ctx, r.Spec.Source)
	if err != nil {
		return nil, fmt.Errorf("golden: resolve the digest of source %s: %w", r.Spec.Source.Ref, err)
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

	if err := b.populate(ctx, r, rootfsDir); err != nil {
		return nil, err
	}

	minSize, err := r.RootMinSizeBytes()
	if err != nil {
		return nil, fmt.Errorf("golden: disk.rootMinSize: %w", err)
	}

	if err := b.opts.Ext4.Build(ctx, rootfsDir, artifactPath, minSize); err != nil {
		return nil, fmt.Errorf("golden: build rootfs for %s: %w", r.Metadata.Name, err)
	}

	// Boot the sealed artifact and hold its guest agent to the v2 handshake.
	// Everything above proves the files are in the rootfs; only this proves the
	// init system starts the agent and that it answers with the protocol and
	// capabilities the template pins. A failed probe leaves no artifact behind,
	// so a later resolve cannot pick up a template that was never proven.
	agentPin, err := b.verifyFirstBoot(ctx, r, kernel, artifactPath)
	if err != nil {
		_ = os.Remove(artifactPath)
		return nil, err
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
		BaseImageDigest:  baseDigest,
		Init:             r.Spec.Init,
		Agent:            agentPin,
		KernelProfile:    r.Spec.Kernel.Profile,
		Pin:              nodePin,
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

// buildPin returns this host's pin and the kernel image the recipe's profile
// resolved to. A recipe that requires other host software is refused here, with
// a typed error, because the alternative is packing a template that only the
// host that built it could use.
func (b *Builder) buildPin(ctx context.Context, r *Recipe) (string, checkpoint.Pin, error) {
	kernel, err := b.kernelFor(r)
	if err != nil {
		return "", checkpoint.Pin{}, err
	}
	pin, err := b.opts.HostPin(ctx, kernel)
	if err != nil {
		return "", checkpoint.Pin{}, err
	}
	if want := r.Spec.Pin; want != nil {
		required := checkpoint.Pin{Firecracker: want.Firecracker, Kernel: want.Kernel}
		if required != pin {
			return "", checkpoint.Pin{}, &PinMismatchError{
				Template: r.Metadata.Name + "@" + r.Metadata.Version,
				Want:     required,
				Got:      pin,
			}
		}
	}
	return kernel, pin, nil
}

// kernelFor returns the kernel image the recipe's kernel profile maps to. A
// profile with no mapping is refused here rather than later, because nothing
// downstream can boot or pin the template without it.
func (b *Builder) kernelFor(r *Recipe) (string, error) {
	kernel := b.opts.KernelProfiles[r.Spec.Kernel.Profile]
	if kernel == "" {
		return "", fmt.Errorf(
			"golden: kernel profile %q of %s@%s is not mapped to a kernel image, so the first boot cannot be verified",
			r.Spec.Kernel.Profile, r.Metadata.Name, r.Metadata.Version)
	}
	return kernel, nil
}

// resolveSourceDigest is the default ResolveSource: an OCI reference is
// resolved through the registry to the digest it currently points at. A source
// type with no registry digest records none, which is honest — the manifest
// then says nothing about the base image's bytes rather than something untrue.
func resolveSourceDigest(ctx context.Context, source Source) (string, error) {
	if source.Type != SourceOCI {
		return "", nil
	}
	return image.ResolveImageDigest(ctx, source.Ref, nil)
}

// populate runs the recipe's provisioning, installs the guest agent, applies
// the recipe's own seal steps and then the standard seal, and verifies both the
// seal and the agent. The agent goes in after provisioning (which may replace
// /sbin or the init system's directories) and before sealing, so a seal script
// sees the finished image; both verifications run last, because a seal step can
// still remove the unit, its runlevel link, or a file the seal is supposed to
// have cleaned.
func (b *Builder) populate(ctx context.Context, r *Recipe, rootfsDir string) error {
	if script := strings.TrimSpace(r.Spec.Provision); script != "" {
		if err := b.opts.Runner.Run(ctx, rootfsDir, script); err != nil {
			return fmt.Errorf("golden: provision %s: %w", r.Metadata.Name, err)
		}
	}

	if err := installAgent(rootfsDir, r.Spec.Init.System, image.AgentBinary()); err != nil {
		return fmt.Errorf("golden: install guest agent for %s: %w", r.Metadata.Name, err)
	}
	if r.Spec.Init.System == InitSysvinit {
		if err := b.opts.Runner.Run(ctx, rootfsDir, sysvStartparScript); err != nil {
			return fmt.Errorf("golden: register guest agent with sysvinit in %s: %w", r.Metadata.Name, err)
		}
	}

	for i, seal := range r.Spec.Seal {
		if strings.TrimSpace(seal) == "" {
			continue
		}
		if err := b.opts.Runner.Run(ctx, rootfsDir, seal); err != nil {
			return fmt.Errorf("golden: seal[%d] %s: %w", i, r.Metadata.Name, err)
		}
	}

	// The standard seal runs after the recipe's own steps, so a recipe cannot
	// leave the image half-clean, and its verification runs before anything is
	// packed, so a template that cannot be sealed is a failed build rather than
	// an almost-clean artifact.
	if err := sealTemplate(rootfsDir); err != nil {
		return fmt.Errorf("golden: seal %s: %w", r.Metadata.Name, err)
	}
	if err := verifySealed(rootfsDir); err != nil {
		return fmt.Errorf("golden: %s: %w", r.Metadata.Name, err)
	}

	if err := verifyAgentInstalled(rootfsDir, r.Spec.Init.System); err != nil {
		return fmt.Errorf("golden: %s would boot without a usable guest agent: %w", r.Metadata.Name, err)
	}
	return nil
}

// reuse returns a result when the artifact and manifest already match digest.
//
// The pin must match too: a template built on other host software is not this
// node's template, and handing it back because its recipe is unchanged would
// record a template the node cannot use. Rebuilding is the answer, not reuse.
func (b *Builder) reuse(artifactPath, metadataPath, digest string, pin checkpoint.Pin) (*Result, bool) {
	info, err := os.Stat(artifactPath)
	if err != nil || info.Size() == 0 {
		return nil, false
	}
	md, err := ReadMetadata(metadataPath)
	if err != nil || md.RecipeDigest != digest || md.Pin != pin {
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

// ReadMetadata, writeMetadataAtomic and fileSHA256 live in manifest.go, with the
// manifest they read and write.
