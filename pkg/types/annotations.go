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

	// AnnotationCpuset carries a cgroup v2 cpuset request (Linux cpulist
	// syntax, e.g. "0-3") for the task's VMM process. It is the task-side form
	// of the "swarmcracker.cpuset" service label (see CpusetLabel).
	AnnotationCpuset = "swarmcracker.cpuset"

	// AnnotationResolver is the nameserver the guest should use, set by the
	// network manager when the task's bridge has a live resolver (dnsmasq) on
	// its gateway. The translator turns it into the sc.dns boot argument, which
	// the guest init wrapper writes to /etc/resolv.conf. It is only a default:
	// an explicit `--dns` on the service always wins.
	AnnotationResolver = "swarmcracker.resolver"

	// DefaultNetworkID is the network ID the executor gives the synthetic
	// attachment it creates for a task that declares no networks, i.e. a task on
	// the node's default bridge. Readers that care which bridge a task is on
	// (the service DNS report) match on it.
	DefaultNetworkID = "default"

	// AnnotationRootfsEphemeral marks a task whose rootfs is a private per-task
	// copy rather than a shared artifact (an image cache file, or a golden
	// image). Only an ephemeral copy is deleted on teardown: the shared cache and
	// golden artifacts must survive the task that used them.
	AnnotationRootfsEphemeral = "rootfs_ephemeral"
)

// UsesPrebuiltRootfs reports whether the task runs a prebuilt golden image.
func (t *Task) UsesPrebuiltRootfs() bool {
	return t != nil && t.Annotations[AnnotationPrebuiltRootfs] == "true"
}
