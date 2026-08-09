package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/moby/swarmkit/v2/api"
)

func listTasks(ctx context.Context, client api.ControlClient) {
	resp, err := client.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list tasks: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Tasks) == 0 {
		fmt.Println("No tasks found")
		return
	}

	fmt.Printf("%-20s %-20s %-12s %-12s %s\n", "ID", "SERVICE", "STATUS", "NODE", "IMAGE")
	fmt.Println(strings.Repeat("-", 80))
	for _, task := range resp.Tasks {
		status := task.Status.State.String()
		nodeID := ""
		if task.NodeID != "" {
			nodeID = task.NodeID[:12]
		}
		svcID := ""
		if task.ServiceID != "" {
			svcID = task.ServiceID[:12]
		}
		image := ""
		if task.Spec.GetContainer() != nil {
			image = task.Spec.GetContainer().Image
		}
		// Show full ID for easier use with inspect
		fmt.Printf("%-20s %-20s %-12s %-12s %s\n", task.ID, svcID, status, nodeID, image)
	}
	fmt.Printf("\nTotal: %d task(s)\n", len(resp.Tasks))
}

// getTaskLogs retrieves logs from a running Firecracker VM.
func getTaskLogs(ctx context.Context, client api.ControlClient, taskID string, lines int) {
	socketDir := "/var/run/firecracker"
	socketPath := filepath.Join(socketDir, taskID+".sock")

	// Check if socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "VM socket not found for task %s\n", taskID)
		os.Exit(1)
	}

	// Read from Firecracker log file (stored in rootfs dir)
	logPath := filepath.Join("/var/lib/firecracker/logs", taskID+".log")
	if _, err := os.Stat(logPath); err == nil {
		content, err := os.ReadFile(logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read log file: %v\n", err)
			os.Exit(1)
		}
		// Print last N lines
		logLines := strings.Split(string(content), "\n")
		start := 0
		if len(logLines) > lines {
			start = len(logLines) - lines
		}
		for i := start; i < len(logLines); i++ {
			fmt.Println(logLines[i])
		}
		return
	}

	// Fallback: try journalctl for the task
	fmt.Printf("No log file found, check: journalctl -u swarmd-* | grep %s\n", taskID)
}

// stopTask stops a running task by sending SIGTERM to its Firecracker process.
func stopTask(ctx context.Context, client api.ControlClient, taskID string) {
	socketPath := filepath.Join("/var/run/firecracker", taskID+".sock")

	// Check if socket exists (VM is running)
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		fmt.Printf("Task %s is not running (socket not found)\n", taskID)
		return
	}

	// Find the Firecracker process
	out, err := exec.Command("pgrep", "-f", taskID).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to find process for task %s: %v\n", taskID, err)
		os.Exit(1)
	}

	pids := strings.Fields(string(out))
	if len(pids) == 0 {
		fmt.Fprintf(os.Stderr, "No process found for task %s\n", taskID)
		os.Exit(1)
	}

	// Send SIGTERM to stop gracefully
	for _, pid := range pids {
		pidInt, _ := strconv.Atoi(pid)
		if err := syscall.Kill(pidInt, syscall.SIGTERM); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to kill process %d: %v\n", pidInt, err)
			continue
		}
		fmt.Printf("Sent SIGTERM to process %d for task %s\n", pidInt, taskID)
	}

	// Wait for process to exit
	time.Sleep(2 * time.Second)

	// Verify stopped
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		fmt.Printf("Task %s stopped successfully\n", taskID)
	} else {
		fmt.Printf("Socket still exists, process may still be running\n")
	}
}
