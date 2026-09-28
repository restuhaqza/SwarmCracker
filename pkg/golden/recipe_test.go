package golden

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validRecipeYAML = `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata:
  name: ubuntu-24.04-docker
  version: 1.0.0
  description: test recipe
spec:
  arch: [amd64, arm64]
  source:
    type: oci
    ref: docker.io/library/ubuntu:24.04
  init:
    system: systemd
    bootArgs:
      - systemd.unified_cgroup_hierarchy=1
  kernel:
    profile: guest-runtime-6.1
  runtime:
    name: docker
    storageDriver: overlay2
    cgroupVersion: v2
    install: packages
  disk:
    rootMinSize: 5GiB
    dataDisk:
      size: 20GiB
      mount: /var/lib/docker
      fs: ext4
  network:
    guestCIDR: 172.17.0.0/16
  provision: |
    set -eux
    apt-get update
  seal:
    - truncate -s 0 /etc/machine-id
  health:
    - command: docker info
      timeout: 60s
  verify:
    - name: overlayfs
      guest: grep -qw overlay /proc/filesystems
`

func TestParseRecipe_Valid(t *testing.T) {
	r, err := ParseRecipe([]byte(validRecipeYAML))
	require.NoError(t, err)
	require.NotNil(t, r)

	assert.Equal(t, "ubuntu-24.04-docker", r.Metadata.Name)
	assert.Equal(t, "1.0.0", r.Metadata.Version)
	assert.Equal(t, "systemd", r.Spec.Init.System)
	assert.Equal(t, "docker", r.Spec.Runtime.Name)
	assert.Equal(t, []string{"amd64", "arm64"}, r.Spec.Arch)
	require.NotNil(t, r.Spec.Disk.DataDisk)
	assert.Equal(t, "/var/lib/docker", r.Spec.Disk.DataDisk.Mount)

	require.NoError(t, r.Validate())

	min, err := r.RootMinSizeBytes()
	require.NoError(t, err)
	assert.Equal(t, int64(5*1<<30), min)
}

func TestParseRecipe_AppliesDefaults(t *testing.T) {
	r, err := ParseRecipe([]byte(`
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata:
  name: minimal
  version: 0.1.0
spec:
  source:
    type: oci
    ref: docker.io/library/busybox:1.36
  kernel:
    profile: firecracker-ci-6.1
`))
	require.NoError(t, err)
	require.NoError(t, r.Validate())

	assert.Equal(t, InitTini, r.Spec.Init.System)
	assert.Equal(t, RuntimeNone, r.Spec.Runtime.Name)
	assert.Equal(t, BuildPreparer, r.Spec.Build, "tini recipes default to the preparer strategy")
	assert.Equal(t, []string{"amd64", "arm64"}, r.Spec.Arch, "arch defaults to both supported")
}

func TestParseRecipe_Errors(t *testing.T) {
	base := `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata:
  name: x
  version: 1
spec:
  source:
    type: oci
    ref: docker.io/library/ubuntu:24.04
  kernel:
    profile: guest-runtime-6.1
`
	tests := []struct {
		name     string
		yaml     string
		wantErr  string
		parseErr bool
	}{
		{
			name:     "malformed yaml",
			yaml:     "kind: [unclosed",
			parseErr: true,
		},
		{
			name: "wrong kind",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: NotGolden
metadata: {name: x, version: "1"}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}}`,
			wantErr: "kind",
		},
		{
			name: "missing name",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {version: "1"}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}}`,
			wantErr: "metadata.name",
		},
		{
			name: "missing version",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}}`,
			wantErr: "metadata.version",
		},
		{
			name: "missing kernel profile",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x, version: "1"}
spec: {source: {type: oci, ref: r}}`,
			wantErr: "kernel.profile",
		},
		{
			name: "unknown source type",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x, version: "1"}
spec: {kernel: {profile: p}, source: {type: ftp, ref: r}}`,
			wantErr: "source.type",
		},
		{
			name: "unknown init system",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x, version: "1"}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}, init: {system: upstart}}`,
			wantErr: "init.system",
		},
		{
			name: "unknown runtime",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x, version: "1"}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}, runtime: {name: lxc}}`,
			wantErr: "runtime.name",
		},
		{
			name: "unsupported arch",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x, version: "1"}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}, arch: [riscv64]}`,
			wantErr: "arch",
		},
		{
			name: "bad disk size",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x, version: "1"}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}, disk: {rootMinSize: "5 furlongs"}}`,
			wantErr: "rootMinSize",
		},
		{
			name: "runtime with tini init",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x, version: "1"}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}, runtime: {name: docker}, init: {system: tini}}`,
			wantErr: "runtime",
		},
		{
			name: "golden build needs a real init",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x, version: "1"}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}, build: golden, init: {system: tini}}`,
			wantErr: "build",
		},
		{
			name: "unknown build strategy",
			yaml: `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: x, version: "1"}
spec: {kernel: {profile: p}, source: {type: oci, ref: r}, build: magic}`,
			wantErr: "build",
		},
	}

	_ = base
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ParseRecipe([]byte(tt.yaml))
			if tt.parseErr {
				require.Error(t, err, "expected a parse error")
				return
			}
			require.NoError(t, err, "recipe should parse; validation is what fails")

			err = r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestRecipe_Digest_StableAndSpecSensitive(t *testing.T) {
	a, err := ParseRecipe([]byte(validRecipeYAML))
	require.NoError(t, err)
	b, err := ParseRecipe([]byte(validRecipeYAML))
	require.NoError(t, err)
	assert.Equal(t, a.Digest(), b.Digest(), "same content must yield same digest")

	// Cosmetic metadata must not change the digest.
	c, err := ParseRecipe([]byte(strings.Replace(validRecipeYAML, "description: test recipe", "description: something else", 1)))
	require.NoError(t, err)
	assert.Equal(t, a.Digest(), c.Digest(), "description is not part of the artifact identity")

	// Changing the build spec must change the digest.
	d, err := ParseRecipe([]byte(strings.Replace(validRecipeYAML, "apt-get update", "apt-get dist-upgrade", 1)))
	require.NoError(t, err)
	assert.NotEqual(t, a.Digest(), d.Digest(), "provision changes must alter the digest")
}

func TestArtifactBaseName(t *testing.T) {
	assert.Equal(t, "golden-ubuntu-24.04-docker-1.0.0", ArtifactBaseName("ubuntu-24.04-docker", "1.0.0"))
}

func TestLoadRecipe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.yaml")
	require.NoError(t, os.WriteFile(path, []byte(validRecipeYAML), 0644))

	r, err := LoadRecipe(path)
	require.NoError(t, err)
	assert.Equal(t, "ubuntu-24.04-docker", r.Metadata.Name)

	_, err = LoadRecipe(filepath.Join(dir, "missing.yaml"))
	assert.Error(t, err)
}
