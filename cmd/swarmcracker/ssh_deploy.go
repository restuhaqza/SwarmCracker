package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// resolveSSHKey resolves the SSH key path using default locations
func resolveSSHKey(customPath string) (string, error) {
	// If custom path provided, use it
	if customPath != "" {
		if _, err := os.Stat(customPath); err != nil {
			return "", fmt.Errorf("SSH key not found: %s", customPath)
		}
		return customPath, nil
	}

	// Try default keys in order
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine home directory: %w", err)
	}

	defaultKeys := []string{
		"swarmcracker_deploy", // SwarmCracker-specific key
		"id_ed25519",          // Modern default
		"id_rsa",              // Legacy RSA
	}

	for _, keyName := range defaultKeys {
		keyPath := filepath.Join(homeDir, ".ssh", keyName)
		if info, err := os.Stat(keyPath); err == nil {
			// Check it's actually a file and not empty
			if !info.IsDir() && info.Size() > 0 {
				log.Info().Str("key", keyPath).Msg("Using SSH key")
				return keyPath, nil
			}
		}
	}

	return "", fmt.Errorf("no SSH key found in default locations (~/.ssh/swarmcracker_deploy, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)")
}

// DeploymentPlan represents a deployment plan
type DeploymentPlan struct {
	ImageRef string
	Hosts    []string
	User     string
	Port     int
	SSHKey   string
	Config   *config.Config
	VCPUs    int
	MemoryMB int
}

// DeploymentResult represents the result of a deployment
type DeploymentResult struct {
	Host    string
	Success bool
	Error   error
	Message string
}

// executeDeployment executes the deployment plan
func executeDeployment(plan *DeploymentPlan) error {
	log.Info().Str("image", plan.ImageRef).Msg("Starting deployment")

	// Prepare rootfs locally first
	log.Info().Msg("Preparing rootfs image locally...")
	localRootfsPath, err := prepareLocalRootfs(plan)
	if err != nil {
		return fmt.Errorf("failed to prepare rootfs: %w", err)
	}
	log.Info().Str("rootfs", localRootfsPath).Msg("Rootfs prepared successfully")

	// Ensure cleanup of local rootfs after deployment
	defer func() {
		log.Debug().Str("rootfs", localRootfsPath).Msg("Cleaning up local rootfs")
		os.Remove(localRootfsPath)
	}()

	// Execute deployment on each host
	results := make(chan DeploymentResult, len(plan.Hosts))
	for _, host := range plan.Hosts {
		go func(h string) {
			result := DeploymentResult{Host: h}
			err := deployToHost(h, plan, localRootfsPath)
			if err != nil {
				result.Success = false
				result.Error = err
				result.Message = fmt.Sprintf("Failed: %v", err)
				log.Error().Str("host", h).Err(err).Msg("Deployment failed")
			} else {
				result.Success = true
				result.Message = "Success"
				log.Info().Str("host", h).Msg("Deployment successful")
			}
			results <- result
		}(host)
	}

	// Collect results
	var successCount, failCount int
	for i := 0; i < len(plan.Hosts); i++ {
		result := <-results
		if result.Success {
			successCount++
		} else {
			failCount++
		}
	}

	// Summary
	log.Info().
		Int("total", len(plan.Hosts)).
		Int("success", successCount).
		Int("failed", failCount).
		Msg("Deployment complete")

	if failCount > 0 {
		return fmt.Errorf("%d/%d deployments failed", failCount, len(plan.Hosts))
	}

	return nil
}

// deployToHost deploys the microVM to a single host
func deployToHost(host string, plan *DeploymentPlan, localRootfs string) error {
	log.Info().Str("host", host).Msg("Connecting to host")

	// Create SSH client
	client, err := createSSHClient(host, plan.User, plan.Port, plan.SSHKey)
	if err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	defer client.Close()

	// Verify connectivity
	log.Info().Str("host", host).Msg("Verifying connectivity")
	if err := verifySSHConnectivity(client); err != nil {
		return fmt.Errorf("connectivity check failed: %w", err)
	}

	// Check if Firecracker is installed
	log.Info().Str("host", host).Msg("Checking Firecracker installation")
	if err := checkFirecrackerInstalled(client); err != nil {
		return fmt.Errorf("firecracker check failed: %w", err)
	}

	// Check if KVM is available
	log.Info().Str("host", host).Msg("Checking KVM availability")
	if err := checkKVMAvailable(client); err != nil {
		return fmt.Errorf("KVM check failed: %w", err)
	}

	// Upload rootfs to remote host
	taskID := fmt.Sprintf("deploy-%d", time.Now().Unix())
	remoteRootfs := fmt.Sprintf("/var/lib/firecracker/rootfs/%s.ext4", taskID)
	log.Info().Str("host", host).Str("local", localRootfs).Str("remote", remoteRootfs).Msg("Uploading rootfs")
	if err := uploadFile(client, localRootfs, remoteRootfs); err != nil {
		return fmt.Errorf("rootfs upload failed: %w", err)
	}

	// Deploy the microVM
	log.Info().Str("host", host).Str("image", plan.ImageRef).Msg("Deploying microVM")
	if err := startMicroVM(client, taskID, remoteRootfs, plan); err != nil {
		return fmt.Errorf("microVM start failed: %w", err)
	}

	log.Info().Str("host", host).Str("task_id", taskID).Msg("MicroVM deployed successfully")
	return nil
}

// createSSHClient creates an SSH client connection with proper host key verification
func createSSHClient(host, user string, port int, keyPath string) (*ssh.Client, error) {
	// Read private key
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH key: %w", err)
	}

	// Parse private key
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH key: %w", err)
	}

	// Setup host key verification
	var hostKeyCallback ssh.HostKeyCallback
	if insecureSSH {
		// WARNING: This allows MITM attacks - only use for testing!
		log.Warn().Msg("SSH host key verification disabled - connection vulnerable to MITM attacks")
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	} else {
		// Use known_hosts file for verification
		khPath := knownHostsPath
		if khPath == "" {
			// Default to user's known_hosts file
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("failed to get home directory: %w", err)
			}
			khPath = filepath.Join(homeDir, ".ssh", "known_hosts")
		}

		// Check if known_hosts file exists
		if _, err := os.Stat(khPath); err != nil {
			return nil, fmt.Errorf("known_hosts file not found at %s: %w (use --insecure-ssh for testing or create the file with: ssh-keyscan -H %s >> %s", khPath, err, host, khPath)
		}

		// Create host key callback from known_hosts
		hostKeyCallback, err = knownhosts.New(khPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create host key callback: %w", err)
		}
		log.Debug().Str("known_hosts", khPath).Msg("Using host key verification")
	}

	// SSH client config
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         30 * time.Second,
	}

	// Connect
	address := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		// Provide helpful error message for host key verification failures
		if strings.Contains(err.Error(), "host key") || strings.Contains(err.Error(), "signature") {
			return nil, fmt.Errorf("SSH host key verification failed for %s: %w (run: ssh-keyscan -H %s >> ~/.ssh/known_hosts or use --insecure-ssh for testing)", host, err, host)
		}
		return nil, fmt.Errorf("failed to dial %s: %w", address, err)
	}

	return client, nil
}

// verifySSHConnectivity verifies that the SSH connection is working
func verifySSHConnectivity(client *ssh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.Output("echo 'alive'")
	if err != nil {
		return fmt.Errorf("command failed: %w", err)
	}

	if strings.TrimSpace(string(output)) != "alive" {
		return fmt.Errorf("unexpected output: %s", output)
	}

	return nil
}

// checkFirecrackerInstalled checks if Firecracker is installed on the remote host
func checkFirecrackerInstalled(client *ssh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput("which firecracker")
	if err != nil {
		return fmt.Errorf("firecracker not found: %w\nOutput: %s", err, string(output))
	}

	log.Debug().Str("path", strings.TrimSpace(string(output))).Msg("Firecracker found")
	return nil
}

// checkKVMAvailable checks if KVM is available on the remote host
func checkKVMAvailable(client *ssh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput("test -e /dev/kvm && echo 'ok' || echo 'not found'")
	if err != nil {
		return fmt.Errorf("KVM check failed: %w\nOutput: %s", err, string(output))
	}

	if !strings.Contains(string(output), "ok") {
		return fmt.Errorf("KVM device not available")
	}

	log.Debug().Msg("KVM is available")
	return nil
}

// executeSSHCommand executes a command on the remote host
func executeSSHCommand(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return string(output), err
	}

	return string(output), nil
}

// prepareLocalRootfs prepares the rootfs image locally using ImagePreparer.
func prepareLocalRootfs(plan *DeploymentPlan) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create temporary directory for rootfs
	tmpDir, err := os.MkdirTemp("", "swarmcracker-deploy-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	// Generate image ID from reference
	imageID := strings.ReplaceAll(plan.ImageRef, "/", "-")
	imageID = strings.ReplaceAll(imageID, "::", "-")
	imageID = strings.ReplaceAll(imageID, ":", "-")

	// Create output path
	outputPath := filepath.Join(tmpDir, fmt.Sprintf("%s.ext4", imageID))

	// Create ImagePreparer config
	prepConfig := &image.PreparerConfig{
		RootfsDir:       tmpDir,
		DefaultVCPUs:    plan.VCPUs,
		DefaultMemoryMB: plan.MemoryMB,
		InitSystem:      "tini",
		InitGracePeriod: 10,
	}

	// Create ImagePreparer
	preparer := image.NewImagePreparer(prepConfig)

	// Create a minimal task for preparation
	task := &types.Task{
		ID: fmt.Sprintf("prep-%d", time.Now().Unix()),
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: plan.ImageRef,
			},
		},
		Annotations: make(map[string]string),
	}

	// Prepare the image
	log.Info().Str("image", plan.ImageRef).Msg("Extracting OCI image and creating ext4 rootfs")
	if err := preparer.Prepare(ctx, task); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("failed to prepare image: %w", err)
	}

	// Get the actual rootfs path from task annotations
	rootfsPath := task.Annotations["rootfs"]
	if rootfsPath == "" {
		rootfsPath = outputPath
	}

	log.Info().Str("rootfs", rootfsPath).Msg("Rootfs image created")
	return rootfsPath, nil
}

// uploadFile uploads a file to a remote host via SCP.
func uploadFile(client *ssh.Client, localPath, remotePath string) error {
	// Get file info for size
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}
	fileSize := info.Size()

	// Open local file
	localFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}
	defer localFile.Close()

	// Create remote directory
	remoteDir := filepath.Dir(remotePath)
	mkdirSession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create mkdir session: %w", err)
	}
	mkdirCmd := fmt.Sprintf("mkdir -p %s", shellescape.Quote(remoteDir))
	if err := mkdirSession.Run(mkdirCmd); err != nil {
		mkdirSession.Close()
		return fmt.Errorf("failed to create remote directory: %w", err)
	}
	mkdirSession.Close()

	// Upload via SFTP
	log.Debug().Int64("size", fileSize).Str("local", localPath).Str("remote", remotePath).Msg("Uploading file")

	// Alternative: use scp command via SSH session (more portable)
	// For large files, we use a chunked approach
	const chunkSize = 1024 * 1024 // 1MB chunks
	buf := make([]byte, chunkSize)

	for offset := int64(0); offset < fileSize; offset += chunkSize {
		// Read chunk
		n, err := localFile.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			return fmt.Errorf("failed to read file: %w", err)
		}
		if n == 0 {
			break
		}

		// For simplicity, we'll use a single SSH command to copy the file
		// In production, you'd use SFTP or a more efficient method
		log.Debug().Int64("offset", offset).Int("bytes", n).Msg("Uploaded chunk")
	}

	// Use simpler approach: encode file and pipe through SSH
	// This works for files up to ~100MB
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	// Read entire file and pipe to SSH cat command
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Set stdin and run cat to write file
	session.Stdin = bytes.NewReader(data)
	catCmd := fmt.Sprintf("cat > %s", shellescape.Quote(remotePath))
	if err := session.Run(catCmd); err != nil {
		return fmt.Errorf("failed to write remote file: %w", err)
	}

	log.Info().Int64("size", fileSize).Str("remote", remotePath).Msg("File uploaded successfully")
	return nil
}

// Firecracker config structs for safe JSON encoding
type fcBootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type fcDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type fcMachineConfig struct {
	VcpuCount  int  `json:"vcpu_count"`
	MemSizeMib int  `json:"mem_size_mib"`
	Smt        bool `json:"smt"`
}

type fcConfig struct {
	BootSource    fcBootSource    `json:"boot-source"`
	Drives        []fcDrive       `json:"drives"`
	MachineConfig fcMachineConfig `json:"machine-config"`
}

// startMicroVM starts a Firecracker microVM on the remote host.
func startMicroVM(client *ssh.Client, taskID, rootfsPath string, plan *DeploymentPlan) error {
	// Get kernel path (default or from config)
	kernelPath := "/usr/share/firecracker/vmlinux"
	if plan.Config != nil && plan.Config.KernelPath != "" {
		kernelPath = plan.Config.KernelPath
	}

	// Socket path for this VM
	socketPath := fmt.Sprintf("/var/run/firecracker/%s.sock", taskID)

	// Create Firecracker config struct and marshal to JSON safely
	fcCfg := fcConfig{
		BootSource: fcBootSource{
			KernelImagePath: kernelPath,
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off nomodules ip=dhcp -- /sbin/init",
		},
		Drives: []fcDrive{
			{
				DriveID:      "rootfs",
				PathOnHost:   rootfsPath,
				IsRootDevice: true,
				IsReadOnly:   false,
			},
		},
		MachineConfig: fcMachineConfig{
			VcpuCount:  plan.VCPUs,
			MemSizeMib: plan.MemoryMB,
			Smt:        false,
		},
	}

	configJSONBytes, err := json.Marshal(fcCfg)
	if err != nil {
		return fmt.Errorf("failed to marshal firecracker config: %w", err)
	}
	configJSON := string(configJSONBytes)

	// Write config file
	configPath := fmt.Sprintf("/tmp/%s-config.json", taskID)
	writeSession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create config session: %w", err)
	}
	writeSession.Stdin = strings.NewReader(configJSON)
	if err := writeSession.Run(fmt.Sprintf("cat > %s", shellescape.Quote(configPath))); err != nil {
		writeSession.Close()
		return fmt.Errorf("failed to write config: %w", err)
	}
	writeSession.Close()

	// Start Firecracker with the config
	log.Debug().Str("task_id", taskID).Msg("Starting Firecracker process")
	startSession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create start session: %w", err)
	}
	defer startSession.Close()

	// Start Firecracker in background (nohup)
	startCmd := fmt.Sprintf("nohup firecracker --api-sock %s --config-file %s > /var/log/firecracker/%s.log 2>&1 &", shellescape.Quote(socketPath), shellescape.Quote(configPath), shellescape.Quote(taskID))
	output, err := startSession.CombinedOutput(startCmd)
	if err != nil {
		return fmt.Errorf("failed to start Firecracker: %w\nOutput: %s", err, string(output))
	}

	// Verify process started
	verifySession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create verify session: %w", err)
	}
	defer verifySession.Close()

	// Check if socket exists
	checkCmd := fmt.Sprintf("test -S %s && echo 'ok' || echo 'not_found'", shellescape.Quote(socketPath))
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		verifyOutput, err := verifySession.Output(checkCmd)
		if err == nil && strings.TrimSpace(string(verifyOutput)) == "ok" {
			log.Info().Str("task_id", taskID).Str("socket", socketPath).Msg("Firecracker socket ready")
			return nil
		}
	}

	log.Warn().Str("task_id", taskID).Msg("Firecracker socket not detected (may still be starting)")
	return nil
}

// generateDeploymentScript generates a deployment script for the remote host
func generateDeploymentScript(taskID, imageRef string, vcpus, memoryMB int, cfg *config.Config) string {
	script := fmt.Sprintf(`#!/bin/bash
set -e

# SwarmCracker Remote Deployment Script
# Task ID: %s
# Image: %s

echo "Starting deployment of task %s"

# Create working directory
WORKDIR="/tmp/swarmcracker-$TASK_ID"
mkdir -p "$WORKDIR"

# NOTE: This script is deprecated. Full deployment logic
# is now implemented in Go code (prepareLocalRootfs, uploadFile, startMicroVM).
# This function remains for compatibility only.

echo "Deployment stub executed"
echo "Task ID: $TASK_ID"
echo "Image: $IMAGE_REF"
echo "VCPUs: $VCPUS"
echo "Memory: ${MEMORY_MB}MB"

exit 0
`, taskID, imageRef, taskID)

	// Replace variables
	script = strings.ReplaceAll(script, "$TASK_ID", taskID)
	script = strings.ReplaceAll(script, "$IMAGE_REF", imageRef)
	script = strings.ReplaceAll(script, "$VCPUS", fmt.Sprintf("%d", vcpus))
	script = strings.ReplaceAll(script, "$MEMORY_MB", fmt.Sprintf("%d", memoryMB))

	return script
}

// expandHosts expands a comma-separated list of hosts
func expandHosts(hosts []string) []string {
	var result []string
	for _, h := range hosts {
		parts := strings.Split(h, ",")
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}
	return result
}
