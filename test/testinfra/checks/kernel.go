package checks

import (
	"fmt"
	"os"
	"syscall"
)

// KernelChecker validates kernel configuration
type KernelChecker struct {
	kernelPath string
	kvmPath    string
}

// NewKernelChecker creates a new kernel checker
func NewKernelChecker() *KernelChecker {
	return &KernelChecker{
		kvmPath: "/dev/kvm",
	}
}

// CheckKVMDevice verifies KVM device is available
func (kc *KernelChecker) CheckKVMDevice() error {
	info, err := os.Stat(kc.kvmPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("KVM device not found at %s", kc.kvmPath)
		}
		return fmt.Errorf("failed to check KVM device: %w", err)
	}

	// Verify it's a character device
	if info.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("%s is not a character device", kc.kvmPath)
	}

	return nil
}

// CheckKVMPermissions verifies user has access to KVM
func (kc *KernelChecker) CheckKVMPermissions() error {
	// Try to open KVM device
	f, err := os.OpenFile(kc.kvmPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("no read/write access to KVM device (try: sudo usermod -aG kvm $USER): %w", err)
	}
	f.Close()

	return nil
}

// CheckKernelModule verifies KVM kernel module is loaded
func (kc *KernelChecker) CheckKernelModule() error {
	// Check if KVM module is loaded
	modules := []string{"kvm", "kvm_intel", "kvm_amd"}

	for _, module := range modules {
		data, err := os.ReadFile(fmt.Sprintf("/sys/module/%s/initstate", module))
		if err == nil {
			state := string(data)
			if state == "live\n" || state == "live" {
				return nil
			}
		}
	}

	return fmt.Errorf("KVM kernel module not loaded. Load with: sudo modprobe kvm_intel (or kvm_amd)")
}

// CheckIOMMU verifies IOMMU is enabled (for device passthrough)
func (kc *KernelChecker) CheckIOMMU() error {
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return fmt.Errorf("failed to read kernel cmdline: %w", err)
	}

	cmdlineStr := string(cmdline)
	if !contains(cmdlineStr, "iommu=") {
		return fmt.Errorf("IOMMU not enabled. Add iommu=pt to kernel cmdline")
	}

	return nil
}

// CheckKVMNested verifies nested virtualization support
func (kc *KernelChecker) CheckKVMNested() error {
	// Check /sys/module/kvm_*/parameters/nested
	modules := []string{"kvm_intel", "kvm_amd"}

	for _, module := range modules {
		nestedPath := fmt.Sprintf("/sys/module/%s/parameters/nested", module)
		data, err := os.ReadFile(nestedPath)
		if err == nil {
			val := string(data)
			if val == "1\n" || val == "Y\n" || val == "1" || val == "Y" {
				return nil
			}
		}
	}

	return fmt.Errorf("nested virtualization not enabled or not supported")
}

// GetKernelVersion returns the current kernel version
func (kc *KernelChecker) GetKernelVersion() (string, error) {
	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err != nil {
		return "", fmt.Errorf("failed to get kernel version: %w", err)
	}

	release := charsToString(uname.Release[:])
	return release, nil
}

// Validate performs all kernel checks
func (kc *KernelChecker) Validate() []error {
	errors := make([]error, 0)

	if err := kc.CheckKVMDevice(); err != nil {
		errors = append(errors, err)
	}

	if err := kc.CheckKVMPermissions(); err != nil {
		errors = append(errors, err)
	}

	if err := kc.CheckKernelModule(); err != nil {
		errors = append(errors, err)
	}

	return errors
}

// charsToString converts byte array to string
func charsToString(ca []int8) string {
	s := make([]byte, len(ca))
	var l int
	for ; l < len(ca); l++ {
		if ca[l] == 0 {
			break
		}
		s[l] = uint8(ca[l])
	}
	return string(s[:l])
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
