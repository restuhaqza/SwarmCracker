package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleSnapshotCommand handles snapshot subcommands.
func handleSnapshotCommand(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: swarmctl snapshot <create|list|restore> ...")
		os.Exit(1)
	}

	snapshotDir := "/var/lib/swarmcracker/snapshots"
	if envState := os.Getenv("SWARM_STATE_DIR"); envState != "" {
		snapshotDir = filepath.Join(envState, "snapshots")
	}

	switch args[0] {
	case "create":
		if len(args) < 3 {
			fmt.Println("Usage: swarmctl snapshot create <task-id> <snapshot-name>")
			os.Exit(1)
		}
		taskID := args[1]
		name := args[2]

		// Create snapshot directory
		os.MkdirAll(snapshotDir, 0755)

		// Find the rootfs for this task
		rootfsDir := "/var/lib/firecracker/rootfs"
		taskRootfs := filepath.Join(rootfsDir, taskID)

		// If task-specific rootfs doesn't exist, try to find it by service
		if _, err := os.Stat(taskRootfs); os.IsNotExist(err) {
			// Check for image-based rootfs
			entries, _ := os.ReadDir(rootfsDir)
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".ext4") {
					taskRootfs = filepath.Join(rootfsDir, entry.Name())
					break
				}
			}
		}

		// Create snapshot metadata
		snapPath := filepath.Join(snapshotDir, name)
		os.MkdirAll(snapPath, 0755)

		meta := map[string]interface{}{
			"name":    name,
			"task_id": taskID,
			"created": time.Now().Format(time.RFC3339),
			"rootfs":  taskRootfs,
		}
		metaJSON, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(snapPath, "meta.json"), metaJSON, 0644)

		// Copy rootfs if it exists
		if _, err := os.Stat(taskRootfs); err == nil {
			srcFile, err := os.Open(taskRootfs)
			if err == nil {
				defer srcFile.Close()
				dstPath := filepath.Join(snapPath, "rootfs.ext4")
				dstFile, err := os.Create(dstPath)
				if err == nil {
					defer dstFile.Close()
					io.Copy(dstFile, srcFile)
					fmt.Printf("Snapshot %s created (copied rootfs from %s)\n", name, taskRootfs)
				} else {
					fmt.Printf("Snapshot %s created (metadata only - could not copy rootfs)\n", name)
				}
			} else {
				fmt.Printf("Snapshot %s created (metadata only)\n", name)
			}
		} else {
			fmt.Printf("Snapshot %s created (metadata only - no rootfs found)\n", name)
		}

	case "list", "ls":
		if _, err := os.Stat(snapshotDir); os.IsNotExist(err) {
			fmt.Println("No snapshots directory")
			return
		}
		entries, err := os.ReadDir(snapshotDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list snapshots: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%-20s %-30s %-20s\n", "NAME", "CREATED", "TASK_ID")
		for _, entry := range entries {
			if entry.IsDir() {
				metaPath := filepath.Join(snapshotDir, entry.Name(), "meta.json")
				if meta, err := os.ReadFile(metaPath); err == nil {
					var m map[string]interface{}
					json.Unmarshal(meta, &m)
					created := ""
					if c, ok := m["created"]; ok {
						created = c.(string)
					}
					taskID := ""
					if t, ok := m["task_id"]; ok {
						taskID = t.(string)
					}
					fmt.Printf("%-20s %-30s %-20s\n", entry.Name(), created, taskID)
				} else {
					fmt.Printf("%-20s %-30s %-20s\n", entry.Name(), "unknown", "unknown")
				}
			}
		}

	case "restore":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl snapshot restore <snapshot-name>")
			os.Exit(1)
		}
		name := args[1]
		snapPath := filepath.Join(snapshotDir, name)
		metaPath := filepath.Join(snapPath, "meta.json")

		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Snapshot %s not found\n", name)
			os.Exit(1)
		}

		meta, err := os.ReadFile(metaPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read snapshot metadata: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Snapshot %s metadata:\n%s\n", name, string(meta))
		fmt.Println("\nTo restore, use the rootfs path in meta.json when creating a new service")

	case "rm", "remove":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl snapshot rm <snapshot-name>")
			os.Exit(1)
		}
		name := args[1]
		snapPath := filepath.Join(snapshotDir, name)
		if err := os.RemoveAll(snapPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to remove snapshot: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Snapshot %s removed\n", name)

	default:
		fmt.Printf("Unknown snapshot command: %s\n", args[0])
		fmt.Println("Available: create, list, restore, rm")
		os.Exit(1)
	}
}
