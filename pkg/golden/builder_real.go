package golden

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/rs/zerolog/log"
)

// OCIExtractor extracts an OCI image (flattened, no SwarmCracker injection)
// into a directory. It supports only the "oci" source type.
type OCIExtractor struct {
	Auth *image.RegistryAuth
}

// NewOCIExtractor returns an extractor using the given registry auth (nil falls
// back to the default keychain).
func NewOCIExtractor(auth *image.RegistryAuth) *OCIExtractor {
	return &OCIExtractor{Auth: auth}
}

// Extract implements Extractor.
func (e *OCIExtractor) Extract(ctx context.Context, source Source, destDir string) error {
	if source.Type != SourceOCI {
		return fmt.Errorf("golden: OCI extractor cannot handle source type %q", source.Type)
	}
	return image.ExtractImageToDir(ctx, source.Ref, destDir, e.Auth)
}

// Ext4Creator packs a directory into an ext4 image via the image package.
type Ext4Creator struct{}

// NewExt4Creator returns an ext4 builder.
func NewExt4Creator() *Ext4Creator { return &Ext4Creator{} }

// Build implements Ext4Builder.
func (e *Ext4Creator) Build(_ context.Context, sourceDir, outputPath string, minSizeBytes int64) error {
	return image.CreateExt4FromDir(sourceDir, outputPath, minSizeBytes)
}

// ChrootRunner runs provisioning and sealing scripts inside a root filesystem.
// It requires root (or CAP_SYS_ADMIN) to bind-mount pseudo-filesystems and
// chroot; it is intended for the builder host, not the guest.
type ChrootRunner struct{}

// NewChrootRunner returns a chroot-based runner.
func NewChrootRunner() *ChrootRunner { return &ChrootRunner{} }

// Run implements Runner.
func (r *ChrootRunner) Run(ctx context.Context, rootfsDir, script string) error {
	mounted, err := mountPseudoFilesystems(ctx, rootfsDir)
	if err != nil {
		unmountAll(mounted)
		return err
	}
	defer unmountAll(mounted)

	if err := ensureBaseDirs(rootfsDir); err != nil {
		unmountAll(mounted)
		return err
	}

	// Keep dpkg/apt postinstalls from trying to start services through a
	// non-running init (standard debootstrap practice).
	cleanupPolicy := ensurePolicyRCD(rootfsDir)
	defer cleanupPolicy()

	cmd := exec.CommandContext(ctx, "chroot", rootfsDir, "/bin/sh", "-c", script)
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"LANG=C",
		"LC_ALL=C",
		"DEBIAN_FRONTEND=noninteractive",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("golden: chroot script failed: %w\n%s", err, tail(string(out), 4096))
	}
	return nil
}

type pseudoMount struct {
	src    string
	dst    string
	fstype string // empty means bind mount
}

func mountPseudoFilesystems(ctx context.Context, rootfsDir string) ([]string, error) {
	mounts := []pseudoMount{
		{src: "/proc", dst: "proc", fstype: "proc"},
		{src: "/sys", dst: "sys", fstype: "sysfs"},
		{src: "/dev", dst: "dev"},
		{src: "/dev/pts", dst: "dev/pts"},
	}

	var mounted []string
	for _, m := range mounts {
		dest := filepath.Join(rootfsDir, m.dst)
		if err := os.MkdirAll(dest, 0755); err != nil {
			return mounted, fmt.Errorf("golden: create mountpoint %s: %w", dest, err)
		}

		args := []string{"--bind", m.src, dest}
		if m.fstype != "" {
			args = []string{"-t", m.fstype, m.src, dest}
		}
		if out, err := exec.CommandContext(ctx, "mount", args...).CombinedOutput(); err != nil {
			return mounted, fmt.Errorf("golden: mount %s -> %s: %w: %s", m.src, dest, err, string(out))
		}
		mounted = append(mounted, dest)
	}

	// Bind the host resolver so package managers inside the chroot have DNS.
	// Best-effort: some images already ship a resolv.conf and some builders have
	// none on the host.
	resolvSrc := "/etc/resolv.conf"
	resolvDst := filepath.Join(rootfsDir, "etc", "resolv.conf")
	if _, err := os.Stat(resolvSrc); err == nil {
		if err := os.MkdirAll(filepath.Dir(resolvDst), 0755); err != nil {
			return mounted, err
		}
		if _, err := os.Stat(resolvDst); err != nil {
			if err := os.WriteFile(resolvDst, nil, 0644); err != nil {
				return mounted, err
			}
		}
		if out, err := exec.CommandContext(ctx, "mount", "--bind", resolvSrc, resolvDst).CombinedOutput(); err != nil {
			log.Warn().Err(err).Str("output", string(out)).
				Msg("Could not bind-mount host resolv.conf into chroot; package downloads may fail")
		} else {
			mounted = append(mounted, resolvDst)
		}
	}

	return mounted, nil
}

// ensureBaseDirs creates the directories package managers expect to exist.
// Minimal OCI images often ship without /tmp, which makes apt/dnf fail.
func ensureBaseDirs(rootfsDir string) error {
	dirs := []struct {
		path string
		mode os.FileMode
	}{
		{"tmp", 0o777 | os.ModeSticky},
		{"var/tmp", 0o777 | os.ModeSticky},
		{"run", 0o755},
		{"var/log", 0o755},
		{"root", 0o700},
	}
	for _, d := range dirs {
		p := filepath.Join(rootfsDir, d.path)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("golden: create %s: %w", p, err)
		}
		if err := os.Chmod(p, d.mode); err != nil {
			log.Warn().Err(err).Str("path", p).Msg("Failed to set directory mode")
		}
	}
	return nil
}

// ensurePolicyRCD installs a policy-rc.d that denies service starts, so package
// managers inside the chroot do not attempt to talk to a non-running init. It
// returns a cleanup func that removes the file again if this call created it.
func ensurePolicyRCD(rootfsDir string) func() {
	path := filepath.Join(rootfsDir, "usr", "sbin", "policy-rc.d")
	if _, err := os.Stat(path); err == nil {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return func() {}
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 101\n"), 0755); err != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

func unmountAll(mounted []string) {
	for i := len(mounted) - 1; i >= 0; i-- {
		if out, err := exec.Command("umount", "-l", mounted[i]).CombinedOutput(); err != nil {
			log.Warn().Err(err).Str("path", mounted[i]).Str("output", string(out)).
				Msg("Failed to unmount pseudo-filesystem")
		}
	}
}

// tail returns the last n bytes of s (useful for bounded error output).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
