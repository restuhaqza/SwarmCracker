// Single source of truth for all site content.
// Components import from here and never hardcode copy.

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type IconName = 'kernel' | 'swarm' | 'kvm' | 'boot' | 'network' | 'update';

export interface NavItem {
  label: string;
  href: string;
}

export interface Feature {
  icon: IconName;
  title: string;
  body: string;
}

export interface Step {
  title: string;
  body: string;
}

export interface QuickstartCmd {
  label: string;
  code: string;
}

export interface ComparisonRow {
  aspect: string;
  swarmcracker: string;
  docker: string;
  kubernetes: string;
}

export interface SecurityPoint {
  title: string;
  body: string;
}

export interface DocsCard {
  title: string;
  body: string;
  href: string;
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

export const links = {
  github: 'https://github.com/restuhaqza/swarmcracker',
  releases: 'https://github.com/restuhaqza/swarmcracker/releases',
  docs: 'https://docs.swarmcracker.com',
  license: 'https://github.com/restuhaqza/swarmcracker/blob/main/LICENSE',
};

// ---------------------------------------------------------------------------
// Site meta
// ---------------------------------------------------------------------------

export const site = {
  name: 'SwarmCracker',
  title: 'SwarmCracker — Firecracker microVMs for SwarmKit',
  description:
    'Firecracker microVMs with SwarmKit orchestration — Docker Swarm UX with hardware-isolated VMs.',
  canonical: 'https://swarmcracker.com/',
  version: 'v0.10.0',
  ogImage: 'https://swarmcracker.com/og-image.png',
  ogImageAlt:
    'SwarmCracker — Firecracker microVMs for SwarmKit, hardware-isolated containers with Docker Swarm UX.',
};

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

export const nav: NavItem[] = [
  { label: 'Features', href: '#features' },
  { label: 'Quickstart', href: '#quickstart' },
  { label: 'Docs', href: links.docs },
  { label: 'GitHub', href: links.github },
];

// ---------------------------------------------------------------------------
// Features
// ---------------------------------------------------------------------------

export const features: Feature[] = [
  {
    icon: 'kernel',
    title: 'Per-VM kernel',
    body: 'Each microVM boots its own Linux kernel, so workloads are fully isolated at the hardware level rather than sharing a host kernel.',
  },
  {
    icon: 'swarm',
    title: 'SwarmKit compatible',
    body: 'Drop-in executor for SwarmKit. Use standard docker service commands to create, scale, and update services backed by Firecracker VMs.',
  },
  {
    icon: 'kvm',
    title: 'KVM hardware isolation',
    body: 'Leverages KVM to provide hardware-enforced boundaries between workloads — stronger than namespace-based container isolation.',
  },
  {
    icon: 'boot',
    title: '~100 ms boot',
    body: 'Firecracker microVMs start in roughly 100 milliseconds, keeping service scaling responsive even under burst traffic.',
  },
  {
    icon: 'network',
    title: 'VXLAN cross-node networking',
    body: 'Built-in VXLAN overlay lets containers on different hosts communicate securely without external CNI plugins.',
  },
  {
    icon: 'update',
    title: 'Rolling updates',
    body: 'Standard SwarmKit rolling-update strategies work out of the box — update images, change resource limits, or roll back with familiar flags.',
  },
];

// ---------------------------------------------------------------------------
// Quickstart steps
// ---------------------------------------------------------------------------

export const steps: Step[] = [
  {
    title: 'Install SwarmCracker',
    body: 'Run the one-line installer to download the binary, kernel image, and rootfs in a single step.',
  },
  {
    title: 'Verify prerequisites',
    body: 'Check that KVM is available, the host meets resource requirements, and required tools are installed.',
  },
  {
    title: 'Set up networking',
    body: 'Create the bridge and TAP device configuration that SwarmCracker uses to connect microVMs.',
  },
  {
    title: 'Deploy a service',
    body: 'Use the familiar docker service create syntax to launch your first container inside a Firecracker microVM.',
  },
];

// ---------------------------------------------------------------------------
// Quickstart commands
// ---------------------------------------------------------------------------

export const quickstart: QuickstartCmd[] = [
  {
    label: 'Install SwarmCracker',
    code: 'curl -fsSL https://swarmcracker.com/install.sh | sudo bash',
  },
  {
    label: 'Check prerequisites',
    code: 'sudo swarmcracker setup check',
  },
  {
    label: 'Download kernel & rootfs',
    code: 'sudo swarmcracker setup install --download-kernel --download-rootfs',
  },
  {
    label: 'Configure networking',
    code: 'sudo swarmcracker setup network',
  },
  {
    label: 'Write default config',
    code: 'sudo swarmcracker setup config --non-interactive',
  },
  {
    label: 'Initialize cluster',
    code: 'sudo swarmcracker cluster init --advertise-addr 192.168.1.10:4242',
  },
  {
    label: 'Create a service',
    code: 'swarmcracker service create --name web --image nginx:alpine --replicas 3',
  },
];

// ---------------------------------------------------------------------------
// Comparison table
// ---------------------------------------------------------------------------

export const comparison: ComparisonRow[] = [
  {
    aspect: 'Isolation',
    swarmcracker: 'KVM hardware virtualization per workload',
    docker: 'Shared kernel via namespaces and cgroups',
    kubernetes: 'Shared kernel via namespaces and cgroups',
  },
  {
    aspect: 'Boot time',
    swarmcracker: '~100 ms (Firecracker microVM)',
    docker: '~50 ms (container start)',
    kubernetes: '~50 ms (container start)',
  },
  {
    aspect: 'Orchestration',
    swarmcracker: 'SwarmKit (built-in)',
    docker: 'Docker Swarm / Compose',
    kubernetes: 'Custom scheduler and control plane',
  },
  {
    aspect: 'Learning curve',
    swarmcracker: 'Familiar Docker Swarm commands',
    docker: 'Familiar Docker commands',
    kubernetes: 'Steeper — YAML manifests, many abstractions',
  },
  {
    aspect: 'Networking',
    swarmcracker: 'Built-in VXLAN overlay',
    docker: 'Overlay / bridge networks',
    kubernetes: 'Requires CNI plugin selection',
  },
];

// ---------------------------------------------------------------------------
// Security points
// ---------------------------------------------------------------------------

export const security: SecurityPoint[] = [
  {
    title: 'Hardware-enforced boundaries',
    body: 'Each workload runs inside its own KVM virtual machine, so a kernel exploit in one VM cannot affect the host or neighbouring VMs.',
  },
  {
    title: 'Minimal attack surface',
    body: 'Firecracker is purpose-built for serverless and microVM workloads — a small codebase with a reduced threat model compared to general-purpose hypervisors.',
  },
  {
    title: 'Jailer support',
    body: "SwarmCracker integrates Firecracker's jailer to further restrict each VMM process with cgroups, seccomp filters, and chroot jails.",
  },
  {
    title: 'Apache 2.0 licensed',
    body: 'Fully open source under a permissive license. Audit the code, contribute fixes, and run it anywhere without vendor lock-in.',
  },
];

// ---------------------------------------------------------------------------
// Docs cards
// ---------------------------------------------------------------------------

export const docsCards: DocsCard[] = [
  {
    title: 'Getting Started',
    body: 'Install SwarmCracker, verify prerequisites, and deploy your first microVM-backed service.',
    href: 'https://docs.swarmcracker.com/user/getting-started/',
  },
  {
    title: 'CLI Reference',
    body: 'Complete reference for every swarmcracker command, flag, and subcommand.',
    href: 'https://docs.swarmcracker.com/user/reference/cli/',
  },
  {
    title: 'Networking',
    body: 'Configure bridges, TAP devices, VXLAN overlays, and cross-node networking.',
    href: 'https://docs.swarmcracker.com/user/guides/networking/',
  },
  {
    title: 'Security',
    body: 'Understand the isolation model, jailer configuration, and security best practices.',
    href: 'https://docs.swarmcracker.com/user/guides/security/',
  },
];

// ---------------------------------------------------------------------------
// Terminal lines (shown in the hero terminal animation)
// ---------------------------------------------------------------------------

export const terminalLines: string[] = [
  '$ swarmcracker service create --name web --image nginx:alpine --replicas 3',
  'creating service "web" (3 replicas)...',
  'pulling image nginx:alpine ...',
  'extracting rootfs ...',
  'creating microVM for task web.1 ...',
  'booting kernel ...',
  'microVM web.1 started (1024 MiB, 2 vCPUs)',
  'creating microVM for task web.2 ...',
  'microVM web.2 started (1024 MiB, 2 vCPUs)',
  'creating microVM for task web.3 ...',
  'microVM web.3 started (1024 MiB, 2 vCPUs)',
  'service "web" converged (3/3 replicas)',
];
