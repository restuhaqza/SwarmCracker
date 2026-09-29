package types

// Task annotations shared between the CLI, executor, and translator.
const (
	// AnnotationRootfs points at the VM's root filesystem image on the host.
	// Set by the image preparer or, for golden images, by the caller.
	AnnotationRootfs = "rootfs"

	// AnnotationPrebuiltRootfs marks a task whose rootfs is a prebuilt golden
	// image that must not be re-prepared and must not be deleted on rollback.
	AnnotationPrebuiltRootfs = "swarmcracker.prebuilt_rootfs"

	// AnnotationGolden records the golden image reference ("name@version").
	AnnotationGolden = "swarmcracker.golden"

	// AnnotationBootArgs carries extra kernel boot arguments for a prebuilt
	// image (e.g. a recipe's systemd/cgroup flags).
	AnnotationBootArgs = "swarmcracker.boot_args"

	// AnnotationKernelProfile names the kernel profile a prebuilt golden image
	// was built against (e.g. "guest-runtime-6.1"). The translator resolves it
	// to a concrete kernel path via the configured kernel profile registry, so
	// a runtime kernel can be selected per image instead of host-wide.
	AnnotationKernelProfile = "swarmcracker.kernel_profile"
)

// UsesPrebuiltRootfs reports whether the task runs a prebuilt golden image.
func (t *Task) UsesPrebuiltRootfs() bool {
	return t != nil && t.Annotations[AnnotationPrebuiltRootfs] == "true"
}
