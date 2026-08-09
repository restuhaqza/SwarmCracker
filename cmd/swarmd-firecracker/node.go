package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moby/swarmkit/v2/log"
	"github.com/moby/swarmkit/v2/node"
	"github.com/restuhaqza/swarmcracker/pkg/swarmkit"
)

// checkAndMigrateClusterAddress checks if the stored cluster address matches
// the desired advertise address. If not, it clears the cluster state to allow
// a fresh start with the new address.
func checkAndMigrateClusterAddress(stateDir, advertiseAddr string) error {
	stateFile := filepath.Join(stateDir, "state.json")

	// Read existing state
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No existing state, nothing to migrate
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	// Parse the state to extract the address
	// The state.json format is: [{"node_id":"...","addr":"..."}]
	type peerEntry struct {
		NodeID string `json:"node_id"`
		Addr   string `json:"addr"`
	}
	var peers []peerEntry
	if err := json.Unmarshal(data, &peers); err != nil {
		return fmt.Errorf("failed to parse state file: %w", err)
	}

	// Check if any peer has a wildcard address that needs migration
	needsMigration := false
	for _, peer := range peers {
		if peer.Addr != "" && strings.HasPrefix(peer.Addr, "0.0.0.0:") {
			needsMigration = true
			fmt.Printf("Detected wildcard address %s, migrating to %s\n", peer.Addr, advertiseAddr)
			break
		}
	}

	if needsMigration {
		// Clear the entire state directory to force a fresh cluster
		// This is necessary because the raft snapshot contains the old address
		// and the CA signer is stored in the raft encrypted state
		fmt.Printf("Clearing cluster state to allow address migration\n")

		// Remove raft state
		raftDir := filepath.Join(stateDir, "raft")
		if err := os.RemoveAll(raftDir); err != nil {
			fmt.Printf("Warning: failed to remove raft state: %v\n", err)
		}

		// Remove certificates to force fresh CA generation
		certDir := filepath.Join(stateDir, "certificates")
		if err := os.RemoveAll(certDir); err != nil {
			fmt.Printf("Warning: failed to remove certificates: %v\n", err)
		}

		// Remove state.json
		if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Warning: failed to remove state file: %v\n", err)
		}

		// Remove worker state
		workerDir := filepath.Join(stateDir, "worker")
		if err := os.RemoveAll(workerDir); err != nil {
			fmt.Printf("Warning: failed to remove worker state: %v\n", err)
		}

		fmt.Printf("Cluster state cleared. A new cluster will be created.\n")
	}

	return nil
}

func startNode(config *node.Config, executor *swarmkit.Executor) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// If this is a manager with a new advertise address, check if we need to
	// clear the old cluster state to use the new address.
	if config.AdvertiseRemoteAPI != "" && config.JoinAddr == "" {
		if err := checkAndMigrateClusterAddress(config.StateDir, config.AdvertiseRemoteAPI); err != nil {
			log.G(ctx).WithError(err).Warn("Failed to check/migrate cluster address")
		}
	}

	// Create node
	n, err := node.New(config)
	if err != nil {
		return fmt.Errorf("failed to create node: %w", err)
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Start the node - this is NON-BLOCKING in SwarmKit
	// It returns immediately after starting internal goroutines
	if err := n.Start(ctx); err != nil {
		return fmt.Errorf("failed to start node: %w", err)
	}

	log.G(ctx).Infof("Node started successfully (hostname=%s, state-dir=%s)", config.Hostname, config.StateDir)

	// Wait for node to be ready (optional but good for debugging)
	select {
	case <-n.Ready():
		log.G(ctx).Info("Node is ready")
	case <-time.After(30 * time.Second):
		log.G(ctx).Warn("Node readiness check timeout (continuing anyway)")
	case <-ctx.Done():
		return fmt.Errorf("context canceled before node was ready")
	}

	// If this is a manager, generate and print join tokens for workers
	if config.JoinAddr == "" {
		printJoinTokens(ctx, config.StateDir)
	}

	// Wait for shutdown signal
	sig := <-sigChan
	log.G(ctx).WithField("signal", sig).Info("Received shutdown signal")

	// Stop the node gracefully
	log.G(ctx).Info("Stopping node...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := n.Stop(shutdownCtx); err != nil {
		log.G(ctx).WithError(err).Error("Failed to stop node gracefully")
		return err
	}

	// Close executor (cleanup dnsmasq, VXLAN)
	if err := executor.Close(); err != nil {
		log.G(ctx).WithError(err).Warn("Failed to close executor")
	}

	// Check if node stopped with any error
	if err := n.Err(ctx); err != nil {
		return fmt.Errorf("node stopped with error: %w", err)
	}

	log.G(ctx).Info("Node stopped successfully")
	return nil
}
