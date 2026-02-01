package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ContainerRuntime defines the interface for container runtime operations
type ContainerRuntime interface {
	CreateContainer(ctx context.Context, imageRef, destPath string) (string, error)
	ExportContainer(ctx context.Context, containerID, tarPath string) error
	RemoveContainer(ctx context.Context, containerID string) error
	PullImage(ctx context.Context, imageRef string) error
	ImageExists(ctx context.Context, imageRef string) bool
}

// FilesystemOperator defines the interface for filesystem operations
type FilesystemOperator interface {
	MkfsExt4(sourceDir, outputPath string) error
	Truncate(path string, sizeMB int) error
	Mount(imagePath, mountDir string) error
	Unmount(mountDir string) error
	CreateFile(path string) error
	RemoveFile(path string) error
	FileExists(path string) bool
	CopyFile(src, dst string, mode os.FileMode) error
}

// BinaryLocator defines the interface for finding system binaries
type BinaryLocator interface {
	LookPath(file string) (string, error)
	Which(file string) (string, error)
	FileExists(path string) bool
}

// RealContainerRuntime implements ContainerRuntime using docker/podman
type RealContainerRuntime struct {
	runtime string
}

func NewRealContainerRuntime(runtime string) ContainerRuntime {
	return &RealContainerRuntime{runtime: runtime}
}

func (r *RealContainerRuntime) CreateContainer(ctx context.Context, imageRef, destPath string) (string, error) {
	var output []byte
	var err error

	if r.runtime == "podman" {
		output, err = exec.CommandContext(ctx, r.runtime, "create", "--root", destPath, imageRef, "/bin/true").CombinedOutput()
	} else {
		output, err = exec.CommandContext(ctx, r.runtime, "create", imageRef, "/bin/true").CombinedOutput()
	}

	if err != nil {
		return "", err
	}
	return string(output), nil
}

func (r *RealContainerRuntime) ExportContainer(ctx context.Context, containerID, tarPath string) error {
	cmd := exec.CommandContext(ctx, r.runtime, "export", containerID, "-o", tarPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	_ = output
	return nil
}

func (r *RealContainerRuntime) RemoveContainer(ctx context.Context, containerID string) error {
	return exec.CommandContext(ctx, r.runtime, "rm", "-f", containerID).Run()
}

func (r *RealContainerRuntime) PullImage(ctx context.Context, imageRef string) error {
	cmd := exec.CommandContext(ctx, r.runtime, "pull", imageRef)
	return cmd.Run()
}

func (r *RealContainerRuntime) ImageExists(ctx context.Context, imageRef string) bool {
	cmd := exec.Command(r.runtime, "image", "inspect", imageRef)
	return cmd.Run() == nil
}

// RealFilesystemOperator implements FilesystemOperator using real system calls
type RealFilesystemOperator struct{}

func NewRealFilesystemOperator() FilesystemOperator {
	return &RealFilesystemOperator{}
}

func (r *RealFilesystemOperator) MkfsExt4(sourceDir, outputPath string) error {
	cmd := exec.Command("mkfs.ext4", "-d", sourceDir, outputPath)
	output, err := cmd.CombinedOutput()
	_ = output
	return err
}

func (r *RealFilesystemOperator) Truncate(path string, sizeMB int) error {
	cmd := exec.Command("truncate", "-s", fmt.Sprintf("%dM", sizeMB), path)
	return cmd.Run()
}

func (r *RealFilesystemOperator) Mount(imagePath, mountDir string) error {
	cmd := exec.Command("mount", "-o", "loop", imagePath, mountDir)
	output, err := cmd.CombinedOutput()
	_ = output
	return err
}

func (r *RealFilesystemOperator) Unmount(mountDir string) error {
	cmd := exec.Command("umount", mountDir)
	return cmd.Run()
}

func (r *RealFilesystemOperator) CreateFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func (r *RealFilesystemOperator) RemoveFile(path string) error {
	return os.Remove(path)
}

func (r *RealFilesystemOperator) FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (r *RealFilesystemOperator) CopyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}

// RealBinaryLocator implements BinaryLocator using system lookups
type RealBinaryLocator struct{}

func NewRealBinaryLocator() BinaryLocator {
	return &RealBinaryLocator{}
}

func (r *RealBinaryLocator) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (r *RealBinaryLocator) Which(file string) (string, error) {
	cmd := exec.Command("which", file)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func (r *RealBinaryLocator) FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// MockContainerRuntime is a mock implementation for testing
type MockContainerRuntime struct {
	Containers map[string]string // containerID -> imageRef
	Images     map[string]bool
	TarFiles   map[string][]byte
	CreateErr  error
	ExportErr  error
	RemoveErr  error
	PullErr    error
	Calls      []string
}

func NewMockContainerRuntime() *MockContainerRuntime {
	return &MockContainerRuntime{
		Containers: make(map[string]string),
		Images:     make(map[string]bool),
		TarFiles:   make(map[string][]byte),
		Calls:      make([]string, 0),
	}
}

func (m *MockContainerRuntime) CreateContainer(ctx context.Context, imageRef, destPath string) (string, error) {
	m.Calls = append(m.Calls, "CreateContainer:"+imageRef)
	if m.CreateErr != nil {
		return "", m.CreateErr
	}
	containerID := "container-" + imageRef
	m.Containers[containerID] = imageRef
	return containerID, nil
}

func (m *MockContainerRuntime) ExportContainer(ctx context.Context, containerID, tarPath string) error {
	m.Calls = append(m.Calls, "ExportContainer:"+containerID)
	if m.ExportErr != nil {
		return m.ExportErr
	}
	m.TarFiles[tarPath] = []byte("mock tar content")
	return nil
}

func (m *MockContainerRuntime) RemoveContainer(ctx context.Context, containerID string) error {
	m.Calls = append(m.Calls, "RemoveContainer:"+containerID)
	if m.RemoveErr != nil {
		return m.RemoveErr
	}
	delete(m.Containers, containerID)
	return nil
}

func (m *MockContainerRuntime) PullImage(ctx context.Context, imageRef string) error {
	m.Calls = append(m.Calls, "PullImage:"+imageRef)
	if m.PullErr != nil {
		return m.PullErr
	}
	m.Images[imageRef] = true
	return nil
}

func (m *MockContainerRuntime) ImageExists(ctx context.Context, imageRef string) bool {
	return m.Images[imageRef]
}

// MockFilesystemOperator is a mock implementation for testing
type MockFilesystemOperator struct {
	Files     map[string][]byte
	Mounts    map[string]string
	MkfsErr   error
	MountErr  error
	UnmountErr error
	CopyErr   error
	Calls     []string
}

func NewMockFilesystemOperator() *MockFilesystemOperator {
	return &MockFilesystemOperator{
		Files:  make(map[string][]byte),
		Mounts: make(map[string]string),
		Calls:  make([]string, 0),
	}
}

func (m *MockFilesystemOperator) MkfsExt4(sourceDir, outputPath string) error {
	m.Calls = append(m.Calls, "MkfsExt4:"+outputPath)
	if m.MkfsErr != nil {
		return m.MkfsErr
	}
	m.Files[outputPath] = []byte("mock ext4 image")
	return nil
}

func (m *MockFilesystemOperator) Truncate(path string, sizeMB int) error {
	m.Calls = append(m.Calls, "Truncate:"+path)
	m.Files[path] = make([]byte, sizeMB*1024*1024)
	return nil
}

func (m *MockFilesystemOperator) Mount(imagePath, mountDir string) error {
	m.Calls = append(m.Calls, "Mount:"+imagePath+":"+mountDir)
	if m.MountErr != nil {
		return m.MountErr
	}
	m.Mounts[imagePath] = mountDir
	return nil
}

func (m *MockFilesystemOperator) Unmount(mountDir string) error {
	m.Calls = append(m.Calls, "Unmount:"+mountDir)
	if m.UnmountErr != nil {
		return m.UnmountErr
	}
	// Remove mount entry
	for img, dir := range m.Mounts {
		if dir == mountDir {
			delete(m.Mounts, img)
		}
	}
	return nil
}

func (m *MockFilesystemOperator) CreateFile(path string) error {
	m.Calls = append(m.Calls, "CreateFile:"+path)
	m.Files[path] = []byte{}
	return nil
}

func (m *MockFilesystemOperator) RemoveFile(path string) error {
	m.Calls = append(m.Calls, "RemoveFile:"+path)
	delete(m.Files, path)
	return nil
}

func (m *MockFilesystemOperator) FileExists(path string) bool {
	_, exists := m.Files[path]
	return exists
}

func (m *MockFilesystemOperator) CopyFile(src, dst string, mode os.FileMode) error {
	m.Calls = append(m.Calls, "CopyFile:"+src+":"+dst)
	if m.CopyErr != nil {
		return m.CopyErr
	}
	if data, exists := m.Files[src]; exists {
		m.Files[dst] = data
		return nil
	}
	return os.ErrNotExist
}

// MockBinaryLocator is a mock implementation for testing
type MockBinaryLocator struct {
	Binaries map[string]string
	WhichErr error
	Calls    []string
}

func NewMockBinaryLocator() *MockBinaryLocator {
	return &MockBinaryLocator{
		Binaries: make(map[string]string),
		Calls:    make([]string, 0),
	}
}

func (m *MockBinaryLocator) LookPath(file string) (string, error) {
	m.Calls = append(m.Calls, "LookPath:"+file)
	if path, exists := m.Binaries[file]; exists {
		return path, nil
	}
	return "", exec.ErrNotFound
}

func (m *MockBinaryLocator) Which(file string) (string, error) {
	m.Calls = append(m.Calls, "Which:"+file)
	if path, exists := m.Binaries[file]; exists {
		return path, m.WhichErr
	}
	return "", m.WhichErr
}

func (m *MockBinaryLocator) FileExists(path string) bool {
	_, exists := m.Binaries[path]
	return exists
}

// ImagePreparerInternal wraps ImagePreparer with testable interfaces
type ImagePreparerInternal struct {
	*ImagePreparer
	runtime  ContainerRuntime
	fsOps    FilesystemOperator
	binLoc   BinaryLocator
}

// NewImagePreparerWithMocks creates an ImagePreparer with custom interfaces
func NewImagePreparerWithMocks(config interface{}, runtime ContainerRuntime, fsOps FilesystemOperator, binLoc BinaryLocator) *ImagePreparerInternal {
	var cfg *PreparerConfig
	if c, ok := config.(*PreparerConfig); ok {
		cfg = c
	} else {
		cfg = &PreparerConfig{
			RootfsDir:       "/var/lib/firecracker/rootfs",
			InitSystem:      "tini",
			InitGracePeriod: 10,
		}
	}

	if cfg.InitSystem == "" {
		cfg.InitSystem = "tini"
	}
	if cfg.InitGracePeriod == 0 {
		cfg.InitGracePeriod = 10
	}

	initConfig := &InitSystemConfig{
		Type:           InitSystemType(cfg.InitSystem),
		GracePeriodSec: cfg.InitGracePeriod,
	}
	initInjector := NewInitInjector(initConfig)

	return &ImagePreparerInternal{
		ImagePreparer: &ImagePreparer{
			config:       cfg,
			cacheDir:     "/var/cache/swarmcracker",
			rootfsDir:    cfg.RootfsDir,
			initInjector: initInjector,
		},
		runtime: runtime,
		fsOps:   fsOps,
		binLoc:  binLoc,
	}
}

// prepareImageWithMocks prepares an image using mocked interfaces
func (ip *ImagePreparerInternal) prepareImageWithMocks(ctx context.Context, imageRef, imageID, outputPath string) error {
	tmpDir := filepath.Join(ip.rootfsDir, "tmp-"+imageID)
	
	// Extract OCI image
	if err := ip.extractOCIImageWithRuntime(ctx, imageRef, tmpDir); err != nil {
		return err
	}

	// Create ext4 image
	if err := ip.fsOps.MkfsExt4(tmpDir, outputPath); err != nil {
		return err
	}

	return nil
}

// extractOCIImageWithRuntime extracts using mocked runtime
func (ip *ImagePreparerInternal) extractOCIImageWithRuntime(ctx context.Context, imageRef, destPath string) error {
	// Create container
	containerID, err := ip.runtime.CreateContainer(ctx, imageRef, destPath)
	if err != nil {
		return err
	}

	// Export to tar
	tarPath := filepath.Join(destPath, "fs.tar")
	if err := ip.runtime.ExportContainer(ctx, containerID, tarPath); err != nil {
		ip.runtime.RemoveContainer(ctx, containerID)
		return err
	}

	// Remove container
	_ = ip.runtime.RemoveContainer(ctx, containerID)

	// Extract tar (simplified - in real implementation would use tar command)
	_ = tarPath

	return nil
}

// injectInitSystemWithMocks injects init using mocked filesystem
func (ip *ImagePreparerInternal) injectInitSystemWithMocks(rootfsPath string) error {
	mountDir, err := ip.mountWithMocks(rootfsPath)
	if err != nil {
		return err
	}
	defer ip.unmountWithMocks(mountDir)

	initBinaryPath := ip.getInitBinaryPathWithLocator()
	if initBinaryPath == "" {
		return nil
	}

	targetPath := filepath.Join(mountDir, ip.initInjector.GetInitPath())
	return ip.fsOps.CopyFile(initBinaryPath, targetPath, 0755)
}

func (ip *ImagePreparerInternal) mountWithMocks(imagePath string) (string, error) {
	mountDir := filepath.Join(ip.rootfsDir, "mount-"+filepath.Base(imagePath))
	if err := ip.fsOps.Mount(imagePath, mountDir); err != nil {
		return "", err
	}
	return mountDir, nil
}

func (ip *ImagePreparerInternal) unmountWithMocks(mountDir string) error {
	return ip.fsOps.Unmount(mountDir)
}

func (ip *ImagePreparerInternal) getInitBinaryPathWithLocator() string {
	paths := []string{
		"/usr/bin/tini",
		"/usr/sbin/tini",
		"/sbin/tini",
	}

	for _, path := range paths {
		if ip.binLoc.FileExists(path) {
			return path
		}
	}

	if path, err := ip.binLoc.LookPath(string(ip.initInjector.config.Type)); err == nil {
		return path
	}

	return ""
}
