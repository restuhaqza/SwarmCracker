package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/log"
	"github.com/moby/swarmkit/v2/manager/allocator/networkallocator"
	"github.com/moby/swarmkit/v2/node"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/restuhaqza/swarmcracker/pkg/cni"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/health"
	"github.com/restuhaqza/swarmcracker/pkg/swarmkit"
	"github.com/urfave/cli/v2"
)

func runAgent(ctx *cli.Context) error {
	// Setup logging
	setupLogging(ctx)

	// Get hostname
	hostname := ctx.String("hostname")
	if hostname == "" {
		var err error
		hostname, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("failed to get hostname: %w", err)
		}
	}

	// Create state directory
	stateDir := ctx.String("state-dir")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Ensure default config exists (auto-generate on first run)
	if created, err := config.EnsureDefaultConfig(); err != nil {
		log.G(context.Background()).Warnf("Failed to create default config: %v (continuing with CLI flags)", err)
	} else if created {
		log.G(context.Background()).Infof("Default config created at %s", config.GetDefaultConfigPath())
	}

	// Create SwarmCracker executor
	natEnabled := ctx.Bool("nat-enabled")
	executorConfig := &swarmkit.Config{
		FirecrackerPath: "firecracker",
		KernelPath:      ctx.String("kernel-path"),
		RootfsDir:       ctx.String("rootfs-dir"),
		Hostname:        hostname,
		JoinAddr:        ctx.String("join-addr"),
		AdvertiseAddr:   ctx.String("advertise-remote-api"), // For managers, use advertise address
		ConsulEnabled:   ctx.Bool("consul-enabled"),
		ConsulAddress:   ctx.String("consul-address"),
		SocketDir:       ctx.String("socket-dir"),
		DefaultVCPUs:    ctx.Int("default-vcpus"),
		DefaultMemoryMB: ctx.Int("default-memory"),
		BridgeName:      ctx.String("bridge-name"),
		Subnet:          ctx.String("subnet"),
		BridgeIP:        ctx.String("bridge-ip"),
		IPMode:          ctx.String("ip-mode"),
		NATEnabled:      &natEnabled,
		VXLANEnabled:    ctx.Bool("vxlan-enabled"),
		VXLANPeers:      parseCommaSeparated(ctx.String("vxlan-peers")),
		// Consul service discovery
		Debug:    ctx.Bool("debug"),
		StateDir: stateDir,
		// Jailer configuration
		EnableJailer:    ctx.Bool("enable-jailer"),
		JailerPath:      ctx.String("jailer-path"),
		JailerUID:       ctx.Int("jailer-uid"),
		JailerGID:       ctx.Int("jailer-gid"),
		JailerChrootDir: ctx.String("jailer-chroot-dir"),
		ParentCgroup:    ctx.String("parent-cgroup"),
		CgroupVersion:   ctx.String("cgroup-version"),
		EnableCgroups:   ctx.Bool("enable-cgroups"),
	}

	fcExecutor, err := swarmkit.NewExecutor(executorConfig)
	if err != nil {
		return fmt.Errorf("failed to create Firecracker executor: %w", err)
	}

	log.G(context.Background()).Infof(
		"SwarmCracker executor initialized (kernel=%s, rootfs=%s, bridge=%s, vxlan=%v, jailer=%v)",
		executorConfig.KernelPath,
		executorConfig.RootfsDir,
		executorConfig.BridgeName,
		executorConfig.VXLANEnabled,
		executorConfig.EnableJailer,
	)

	if len(executorConfig.VXLANPeers) > 0 {
		log.G(context.Background()).Infof("VXLAN peers configured: %v", executorConfig.VXLANPeers)
	} else if executorConfig.VXLANEnabled {
		log.G(context.Background()).Warn("VXLAN enabled but no peers configured — " +
			"use --vxlan-peers for static peers or --consul-enabled for dynamic discovery")
		log.G(context.Background()).Info("For small clusters (≤5 nodes), static peers are sufficient:\n" +
			"  --vxlan-peers 192.168.1.11,192.168.1.12")
	}

	// Start health + metrics server
	healthChecker := health.NewChecker(ctx.String("bridge-name"), "firecracker")
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/healthz", healthChecker)
		mux.Handle("/metrics", promhttp.Handler())
		healthAddr := ctx.String("health-addr")

		// Wrap with security middleware: rate limiting + request timeout
		handler := withRequestTimeout(withRateLimit(mux, 10, 20), 5*time.Second)

		server := &http.Server{
			Addr:         healthAddr,
			Handler:      handler,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  30 * time.Second,
		}

		log.G(context.Background()).Infof("Starting health check server on %s", healthAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.G(context.Background()).WithError(err).Warn("Health check server failed")
		}
	}()

	// Create CNI network provider if enabled
	var networkProvider networkallocator.Provider
	var networkConfig *networkallocator.Config

	if ctx.Bool("enable-cni") {
		cniConfig := &cni.CNIConfig{
			BridgeName:   ctx.String("bridge-name"),
			SubnetPool:   ctx.String("cni-subnet-pool"),
			SubnetSize:   ctx.Int("cni-subnet-size"),
			VXLANPort:    uint32(ctx.Int("cni-vxlan-port")),
			IPAMType:     "host-local",
			PluginDir:    ctx.String("cni-plugin-dir"),
			ConfigDir:    ctx.String("cni-config-dir"),
			EnableIPMasq: true,
		}

		cniProvider, err := cni.NewCNIProvider(cniConfig)
		if err != nil {
			return fmt.Errorf("failed to create CNI provider: %w", err)
		}

		networkProvider = cniProvider
		networkConfig = &networkallocator.Config{
			DefaultAddrPool: []string{cniConfig.SubnetPool},
			SubnetSize:      uint32(cniConfig.SubnetSize),
			VXLANUDPPort:    cniConfig.VXLANPort,
		}

		log.G(context.Background()).Infof(
			"CNI network provider enabled (plugin-dir=%s, config-dir=%s, pool=%s, vxlan-port=%d)",
			cniConfig.PluginDir,
			cniConfig.ConfigDir,
			cniConfig.SubnetPool,
			cniConfig.VXLANPort,
		)
	} else {
		log.G(context.Background()).Warn("CNI network provider NOT enabled - worker nodes will fail to join")
		log.G(context.Background()).Warn("Use --enable-cni flag to enable network allocation")
	}

	// Create node config
	nodeConfig := &node.Config{
		Hostname:           hostname,
		StateDir:           stateDir,
		JoinAddr:           ctx.String("join-addr"),
		JoinToken:          ctx.String("join-token"),
		ListenRemoteAPI:    ctx.String("listen-remote-api"),
		ListenControlAPI:   ctx.String("listen-control-api"),
		AdvertiseRemoteAPI: ctx.String("advertise-remote-api"),
		Executor:           fcExecutor,
		NetworkProvider:    networkProvider, // ← CNI Provider
		NetworkConfig:      networkConfig,   // ← Network config
		ForceNewCluster:    ctx.Bool("force-new-cluster"),
		HeartbeatTick:      uint32(ctx.Int("heartbeat-tick")),
		ElectionTick:       uint32(ctx.Int("election-tick")),
		Availability:       api.NodeAvailabilityActive,
	}

	// Start node
	if err := startNode(nodeConfig, fcExecutor); err != nil {
		return fmt.Errorf("failed to start node: %w", err)
	}

	return nil
}
