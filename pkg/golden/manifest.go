package golden

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
)

// This file is the template contract: the manifest that describes a sealed,
// versioned template, the digest that identifies its inputs, and the pin that
// says which host software the template is valid against.
//
// Three questions a node asks about a template, answered by three different
// things in the manifest:
//
//   - "Is this the template I asked for?" — the digest (Recipe.Digest), over
//     the recipe's canonical form. It is computed before the build starts, so
//     an up-to-date artifact is reused without touching the network.
//   - "Can I drive the agent inside it?" — the agent pin, verified at build
//     time against the agent that actually answered (CheckAgentPin).
//   - "Was it built by software I run?" — the pin, checked with VerifyPin.
//
// # What the digest covers, and what it deliberately does not
//
// The digest is a claim that two builds of the same recipe are the same
// template, so it must be identical on any host and must change when the
// template's content changes. It is the SHA-256 of the canonical JSON document
// Digest writes, which is:
//
// IN (content, in the recipe)
//
//	metadata.name, metadata.version
//	spec.build, spec.arch
//	spec.source.type, spec.source.ref (the reference as written)
//	spec.init.system, spec.init.bootArgs
//	spec.kernel.profile
//	spec.runtime.*, spec.disk.*, spec.network.*
//	spec.agent.protocol, spec.agent.version
//	spec.pin.* if the recipe requires one
//	spec.provision, spec.seal (in order), spec.health, spec.verify
//	BuilderRevision — the pipeline that produced the artifact
//
// OUT (build environment, in the manifest but never in the digest)
//
//	metadata.description — cosmetic
//	spec.workDir, output directory, kernel path, host name — this host
//	the Firecracker version — this host's VMM (see Pin)
//	baseImageDigest — what the source resolved to at this build (see below)
//	builtAt, artifact file name, sizeBytes, sha256 — this build's result
//
// Two consequences worth stating plainly, because they are the limits of the
// guarantee:
//
//   - Same digest does not mean same base image bytes. A recipe that names a
//     moving tag keeps its digest when the tag moves, and the artifact changes.
//     BaseImageDigest is the recorded field that tells those two builds apart;
//     a consumer that needs the stronger guarantee compares it in addition to
//     the digest. The digest stays offline-computable on purpose, because that
//     is what lets Build reuse an up-to-date artifact without a registry call.
//   - The pin is out of the digest even though it is build-dependent, because
//     a template that needed a different Firecracker version is the same
//     template. What is *not* allowed is reusing a template built on other
//     host software as if this node had built it; the builder refuses to reuse
//     one whose pin differs (see Builder.reuse).

// HostPin is the host software a recipe requires its template to be built on.
// A build on other software cannot produce a template the recipe vouches for,
// so it is refused rather than recorded: the resulting artifacts would be
// resumed only where they were built, which is the failure M4 exists to remove.
//
// Both fields are required when the block is present, and both are compared
// exactly, for the reason checkpoint.Pin compares exactly: a version match says
// nothing about the kernel, and an approximate match is no match.
type HostPin struct {
	// Firecracker is the VMM version the build must run, as
	// `firecracker --version` reports it (e.g. "v1.15.1").
	Firecracker string `yaml:"firecracker,omitempty" json:"firecracker,omitempty"`
	// Kernel is the identity of the kernel image the recipe's profile must
	// resolve to. A SHA-256 is what tells two kernels apart when their version
	// strings agree, so that is what a build records and what this compares.
	Kernel string `yaml:"kernel,omitempty" json:"kernel,omitempty"`
}

// CodeTemplatePinMismatch is the stable code for a template pinned to host
// software this node does not run. It is the contract; the message next to it
// is not.
const CodeTemplatePinMismatch = "template_pin_mismatch"

// PinMismatchError reports a template that cannot be used here: it was built by
// a Firecracker version or a guest kernel this node does not run, or the recipe
// requires host software this node does not have.
//
// It carries checkpoint.ErrPinMismatch, so a caller that already branches on
// that class for a checkpoint branches on it for a template too — the same two
// identities, checked the same way.
type PinMismatchError struct {
	// Template names the template ("name@version"), or the recipe being built.
	Template string
	// Want is what this node runs: the "this node runs" side of the message, in a
	// use and in a build alike.
	Want checkpoint.Pin
	// Got is what the template is held to: the "is pinned to" side of the message.
	// It is the pin the template carries, or — at build time, where no template
	// exists yet — the pin the recipe requires, which is what a template built here
	// would carry.
	Got checkpoint.Pin
}

// Error implements error.
//
// The sentence names the two sides of the disagreement in the roles the fields
// hold: what the template is pinned to, then what this node runs. A template that
// carries no pin says that, rather than being pinned to a value called "unpinned".
func (e *PinMismatchError) Error() string {
	pinned := fmt.Sprintf("is pinned to Firecracker %q with kernel %q", e.Got.Firecracker, e.Got.Kernel)
	if e.Got.Firecracker == "" && e.Got.Kernel == "" {
		pinned = "carries no pin"
	}
	want := fmt.Sprintf("Firecracker %q with kernel %q", e.Want.Firecracker, e.Want.Kernel)
	return fmt.Sprintf("%s: %s %s, this node runs %s: use it only where it was built, or build a new template here",
		CodeTemplatePinMismatch, templateName(e.Template), pinned, want)
}

// Unwrap exposes checkpoint.ErrPinMismatch, the class a checkpoint reports for
// the same disagreement.
func (e *PinMismatchError) Unwrap() error { return checkpoint.ErrPinMismatch }

// Code returns the stable failure code, so a caller can branch without matching
// message text.
func (e *PinMismatchError) Code() string { return CodeTemplatePinMismatch }

// templateName names a template in a message, without inventing one for a
// zero value.
func templateName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "template"
	}
	return name
}

// ArtifactMetadata is the sidecar written next to every artifact: the template
// manifest. It is what Resolve reads, what a node decides with, and what
// `template inspect` prints, and it is one document so a reader learns
// everything about a template without touching the artifact.
//
// Every field is something the build observed rather than something it was
// told: the digest of the recipe it was built from, the pin of the host that
// built it, the digest the source resolved to, the agent that answered on first
// boot, and the artifact's own digest.
type ArtifactMetadata struct {
	APIVersion   string   `json:"apiVersion"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	RecipeDigest string   `json:"recipeDigest"`
	Arch         []string `json:"arch"`
	Source       Source   `json:"source"`
	// BaseImageDigest is the digest the recipe's source resolved to at this
	// build ("sha256:..."), so two builds that share a recipeDigest can still
	// be told apart when a moving tag moved. It is empty for a source type
	// with no registry digest.
	BaseImageDigest string   `json:"baseImageDigest,omitempty"`
	Init            Init     `json:"init"`
	Agent           AgentPin `json:"agent"`
	KernelProfile   string   `json:"kernelProfile"`
	// Pin is the host software this template is valid against: the Firecracker
	// version and kernel it was built and proven on. It is the same pin the
	// checkpoint package checks, so a checkpoint taken from a VM booted from
	// this template names the same two identities.
	Pin              checkpoint.Pin `json:"pin"`
	Runtime          Runtime        `json:"runtime"`
	RootMinSizeBytes int64          `json:"rootMinSizeBytes"`
	Artifact         string         `json:"artifact"`
	SizeBytes        int64          `json:"sizeBytes"`
	SHA256           string         `json:"sha256"`
	BuiltAt          time.Time      `json:"builtAt"`
}

// Ref returns the canonical "name@version" reference of the template.
func (m *ArtifactMetadata) Ref() string {
	if m == nil {
		return ""
	}
	return m.Name + "@" + m.Version
}

// VerifyPin reports whether this node can use the template: pin is what the
// node runs, and a template built on anything else is refused with
// *PinMismatchError, which carries checkpoint.ErrPinMismatch.
//
// Both identities must agree. A Firecracker version match says nothing about
// the kernel the memory image was built against, and an unpinned template — one
// built before pinning existed — says nothing at all, so it is refused rather
// than treated as a wildcard.
//
// This is the check the checkpoint package makes on the same two values
// (checkpoint.Manifest.VerifyPin): a template and the checkpoints taken from it
// are pinned to the same software, so a node that refuses one refuses the other.
func (m *ArtifactMetadata) VerifyPin(pin checkpoint.Pin) error {
	if m == nil {
		return errors.New("golden: nil template manifest")
	}
	if err := pin.Validate(); err != nil {
		return fmt.Errorf("golden: this node's pin is incomplete: %w", err)
	}
	if m.Pin == pin {
		return nil
	}
	return &PinMismatchError{Template: m.Name + "@" + m.Version, Want: pin, Got: m.Pin}
}

// ReadMetadata loads a template manifest from an artifact's sidecar.
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

// writeMetadataAtomic writes JSON to path via a temp file and rename, so a
// reader sees the previous manifest or the new one and never a half-written
// one.
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

// DefaultFirecrackerBinary is the VMM a build pins when no other is named.
const DefaultFirecrackerBinary = "firecracker"

// NodePin reports the host software a template built here is valid against: the
// version of the Firecracker binary that will boot it, and the SHA-256 of the
// kernel image it will be booted with.
//
// Both are read from the host rather than declared, because a pin that merely
// repeats the recipe would prove nothing. The kernel is hashed rather than
// named, because its path is this host's and its version string does not
// separate two builds of the same kernel.
func NodePin(ctx context.Context, firecrackerBinary, kernelPath string) (checkpoint.Pin, error) {
	if strings.TrimSpace(kernelPath) == "" {
		return checkpoint.Pin{}, errors.New("golden: no kernel image to pin: the recipe's kernel profile must resolve to one")
	}
	kernel, err := fileSHA256(kernelPath)
	if err != nil {
		return checkpoint.Pin{}, fmt.Errorf("golden: hash the kernel image %s: %w", kernelPath, err)
	}
	version, err := firecrackerVersion(ctx, firecrackerBinary)
	if err != nil {
		return checkpoint.Pin{}, err
	}
	return checkpoint.Pin{Firecracker: version, Kernel: kernel}, nil
}

// firecrackerVersion returns the version a Firecracker binary reports. The
// version string is what Firecracker's own snapshot state compatibility is
// keyed on, so it is taken from the binary that is about to be run and not from
// the recipe.
func firecrackerVersion(ctx context.Context, binary string) (string, error) {
	if strings.TrimSpace(binary) == "" {
		binary = DefaultFirecrackerBinary
	}
	out, err := exec.CommandContext(ctx, binary, "--version").Output() //nolint:gosec // binary is operator-configured
	if err != nil {
		return "", fmt.Errorf("golden: read the version of the Firecracker binary %s: %w", binary, err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.EqualFold(fields[0], "Firecracker") {
		return "", fmt.Errorf("golden: %s --version printed %q: want a version line like \"Firecracker v1.15.1\"", binary, strings.TrimSpace(line))
	}
	return fields[1], nil
}
