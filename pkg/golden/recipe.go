// Package golden implements the SwarmCracker golden image recipe format and
// the builder that turns a recipe into a sealed, bootable ext4 artifact.
//
// A recipe is a declarative build input (see recipes/*.yaml): it names a base
// userspace, the init system to boot, the container runtime to install inside
// the guest, a kernel profile, disk sizing, and the provisioning/sealing
// scripts to run before the root filesystem is packed.
package golden

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/image"
	"gopkg.in/yaml.v3"
)

const (
	// APIVersionV1Alpha1 is the only supported recipe API version.
	APIVersionV1Alpha1 = "swarmcracker.io/v1alpha1"
	// KindGoldenImage is the only supported recipe kind.
	KindGoldenImage = "GoldenImage"
)

// Init systems supported by a recipe.
const (
	InitSystemd  = "systemd"
	InitOpenRC   = "openrc"
	InitSysvinit = "sysvinit"
	InitCustom   = "custom"
	InitTini     = "tini"
)

// Source types supported by a recipe.
const (
	SourceOCI        = "oci"
	SourceRootfsTar  = "rootfs-tar"
	SourceCloudImage = "cloud-image"
)

// Container runtimes supported inside a golden image.
const (
	RuntimeDocker     = "docker"
	RuntimeContainerd = "containerd"
	RuntimePodman     = "podman"
	RuntimeNone       = "none"
)

// Build strategies. "golden" is produced by the golden image builder; "preparer"
// describes the legacy single-workload model, which is assembled at task time by
// the image preparer (init/essential-file injection) and cannot be built here.
const (
	BuildGolden   = "golden"
	BuildPreparer = "preparer"
)

var (
	namePattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
)

// Recipe is a golden image definition.
type Recipe struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

// Metadata identifies an artifact.
type Metadata struct {
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Spec is the build specification.
type Spec struct {
	Build     string        `yaml:"build,omitempty" json:"build,omitempty"`
	Arch      []string      `yaml:"arch,omitempty" json:"arch,omitempty"`
	Source    Source        `yaml:"source" json:"source"`
	Init      Init          `yaml:"init,omitempty" json:"init,omitempty"`
	Kernel    Kernel        `yaml:"kernel" json:"kernel"`
	Runtime   Runtime       `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Disk      Disk          `yaml:"disk,omitempty" json:"disk,omitempty"`
	Network   Network       `yaml:"network,omitempty" json:"network,omitempty"`
	Provision string        `yaml:"provision,omitempty" json:"provision,omitempty"`
	Seal      []string      `yaml:"seal,omitempty" json:"seal,omitempty"`
	Health    []HealthCheck `yaml:"health,omitempty" json:"health,omitempty"`
	Verify    []VerifyCheck `yaml:"verify,omitempty" json:"verify,omitempty"`
}

// Source describes how to obtain the base userspace.
type Source struct {
	Type string `yaml:"type" json:"type"`
	Ref  string `yaml:"ref" json:"ref"`
}

// Init describes the guest init system and extra boot args.
type Init struct {
	System   string   `yaml:"system,omitempty" json:"system,omitempty"`
	BootArgs []string `yaml:"bootArgs,omitempty" json:"bootArgs,omitempty"`
}

// Kernel selects the guest kernel profile.
type Kernel struct {
	Profile  string `yaml:"profile" json:"profile"`
	MustBoot bool   `yaml:"mustBoot,omitempty" json:"mustBoot,omitempty"`
}

// Runtime describes the container runtime installed inside the guest.
type Runtime struct {
	Name          string `yaml:"name,omitempty" json:"name,omitempty"`
	Version       string `yaml:"version,omitempty" json:"version,omitempty"`
	StorageDriver string `yaml:"storageDriver,omitempty" json:"storageDriver,omitempty"`
	CgroupVersion string `yaml:"cgroupVersion,omitempty" json:"cgroupVersion,omitempty"`
	Install       string `yaml:"install,omitempty" json:"install,omitempty"`
}

// Disk describes rootfs sizing and an optional data disk.
type Disk struct {
	RootMinSize string    `yaml:"rootMinSize,omitempty" json:"rootMinSize,omitempty"`
	DataDisk    *DataDisk `yaml:"dataDisk,omitempty" json:"dataDisk,omitempty"`
}

// DataDisk describes an optional second writable drive (e.g. the image store).
type DataDisk struct {
	Size  string `yaml:"size,omitempty" json:"size,omitempty"`
	Mount string `yaml:"mount,omitempty" json:"mount,omitempty"`
	FS    string `yaml:"fs,omitempty" json:"fs,omitempty"`
}

// Network holds in-guest network settings.
type Network struct {
	GuestCIDR string `yaml:"guestCIDR,omitempty" json:"guestCIDR,omitempty"`
}

// HealthCheck is a post-boot readiness command.
type HealthCheck struct {
	Command string   `yaml:"command" json:"command"`
	Timeout Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// VerifyCheck is a boot-time assertion used by CI.
type VerifyCheck struct {
	Name  string `yaml:"name" json:"name"`
	Guest string `yaml:"guest" json:"guest"`
}

// Duration wraps time.Duration so YAML values like "60s" parse.
type Duration time.Duration

// UnmarshalYAML parses string durations ("60s", "2m") and numeric seconds.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err == nil {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var num int64
	if err := value.Decode(&num); err != nil {
		return fmt.Errorf("duration must be a string or number: %v", err)
	}
	*d = Duration(time.Duration(num) * time.Second)
	return nil
}

// Std returns the wrapped time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// ParseRecipe parses recipe YAML. It does not validate; call Validate.
func ParseRecipe(data []byte) (*Recipe, error) {
	var r Recipe
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("golden: parse recipe: %w", err)
	}
	return &r, nil
}

// LoadRecipe reads and parses a recipe file.
func LoadRecipe(path string) (*Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("golden: read recipe %s: %w", path, err)
	}
	return ParseRecipe(data)
}

// Validate checks the recipe and applies defaults in place. It reports every
// problem it finds rather than stopping at the first.
func (r *Recipe) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	r.applyDefaults()
	r.validateMetadata(add)
	r.validateSource(add)
	r.validateBuild(add)
	r.validateInit(add)
	r.validateRuntime(add)
	r.validateDisk(add)

	if len(problems) > 0 {
		return fmt.Errorf("golden: invalid recipe: %s", strings.Join(problems, "; "))
	}
	return nil
}

// applyDefaults fills in zero-value spec fields so later validation sees the
// effective configuration.
func (r *Recipe) applyDefaults() {
	if len(r.Spec.Arch) == 0 {
		r.Spec.Arch = []string{"amd64", "arm64"}
	}
	if r.Spec.Init.System == "" {
		r.Spec.Init.System = InitTini
	}
	if r.Spec.Runtime.Name == "" {
		r.Spec.Runtime.Name = RuntimeNone
	}
	if r.Spec.Build == "" {
		// Single-workload images (injected tini) are assembled by the preparer.
		if r.Spec.Init.System == InitTini || r.Spec.Init.System == InitCustom {
			r.Spec.Build = BuildPreparer
		} else {
			r.Spec.Build = BuildGolden
		}
	}
}

// validateMetadata checks the API version, kind, and artifact identity.
func (r *Recipe) validateMetadata(add func(string, ...any)) {
	if r.APIVersion != APIVersionV1Alpha1 {
		add("apiVersion must be %q, got %q", APIVersionV1Alpha1, r.APIVersion)
	}
	if r.Kind != KindGoldenImage {
		add("kind must be %q, got %q", KindGoldenImage, r.Kind)
	}
	if r.Metadata.Name == "" {
		add("metadata.name is required")
	} else if !namePattern.MatchString(r.Metadata.Name) {
		add("metadata.name %q must be lowercase alphanumeric with . _ - separators", r.Metadata.Name)
	}
	if r.Metadata.Version == "" {
		add("metadata.version is required")
	} else if !versionPattern.MatchString(r.Metadata.Version) {
		add("metadata.version %q contains invalid characters", r.Metadata.Version)
	}
}

// validateSource checks the supported architectures, base source, and kernel.
func (r *Recipe) validateSource(add func(string, ...any)) {
	for _, a := range r.Spec.Arch {
		if a != "amd64" && a != "arm64" {
			add("arch %q is not supported (want amd64 or arm64)", a)
		}
	}

	switch r.Spec.Source.Type {
	case SourceOCI, SourceRootfsTar, SourceCloudImage:
	default:
		add("source.type %q is not supported (want %s, %s, or %s)",
			r.Spec.Source.Type, SourceOCI, SourceRootfsTar, SourceCloudImage)
	}
	if r.Spec.Source.Ref == "" {
		add("source.ref is required")
	}

	if r.Spec.Kernel.Profile == "" {
		add("kernel.profile is required")
	}
}

// validateBuild checks the build strategy is supported and consistent with the
// chosen init system.
func (r *Recipe) validateBuild(add func(string, ...any)) {
	switch r.Spec.Build {
	case BuildGolden:
		if r.Spec.Init.System == InitTini || r.Spec.Init.System == InitCustom {
			add("build %q requires a real init (systemd, openrc, or sysvinit), got %q",
				BuildGolden, r.Spec.Init.System)
		}
	case BuildPreparer:
	default:
		add("build %q is not supported (want %s or %s)", r.Spec.Build, BuildGolden, BuildPreparer)
	}
}

// validateInit checks the guest init system is supported.
func (r *Recipe) validateInit(add func(string, ...any)) {
	switch r.Spec.Init.System {
	case InitSystemd, InitOpenRC, InitSysvinit, InitCustom, InitTini:
	default:
		add("init.system %q is not supported", r.Spec.Init.System)
	}
}

// validateRuntime checks the container runtime is supported and consistent with
// the chosen init system.
func (r *Recipe) validateRuntime(add func(string, ...any)) {
	switch r.Spec.Runtime.Name {
	case RuntimeDocker, RuntimeContainerd, RuntimePodman:
		if r.Spec.Init.System == InitTini || r.Spec.Init.System == InitCustom {
			add("runtime %q requires a real init (systemd, openrc, or sysvinit), got %q",
				r.Spec.Runtime.Name, r.Spec.Init.System)
		}
	case RuntimeNone:
	default:
		add("runtime.name %q is not supported", r.Spec.Runtime.Name)
	}
}

// validateDisk checks the optional sizing fields parse as disk sizes.
func (r *Recipe) validateDisk(add func(string, ...any)) {
	if r.Spec.Disk.RootMinSize != "" {
		if _, err := image.ParseDiskSize(r.Spec.Disk.RootMinSize); err != nil {
			add("disk.rootMinSize: %v", err)
		}
	}
	if r.Spec.Disk.DataDisk != nil && r.Spec.Disk.DataDisk.Size != "" {
		if _, err := image.ParseDiskSize(r.Spec.Disk.DataDisk.Size); err != nil {
			add("disk.dataDisk.size: %v", err)
		}
	}
}

// Digest returns a stable content hash of the artifact identity (name, version,
// and build spec). Cosmetic metadata such as the description is excluded.
func (r *Recipe) Digest() string {
	payload := struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Spec    Spec   `json:"spec"`
	}{r.Metadata.Name, r.Metadata.Version, r.Spec}

	data, err := json.Marshal(payload)
	if err != nil {
		// Struct marshalling cannot fail for these types.
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// RootMinSizeBytes returns the configured rootfs floor in bytes (0 if unset).
func (r *Recipe) RootMinSizeBytes() (int64, error) {
	return image.ParseDiskSize(r.Spec.Disk.RootMinSize)
}

// ArtifactBaseName returns the shared filename stem for a recipe's artifacts.
func ArtifactBaseName(name, version string) string {
	return fmt.Sprintf("golden-%s-%s", name, version)
}
