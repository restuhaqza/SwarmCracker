package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// handleVolumeCommand handles volume subcommands.
func handleVolumeCommand(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: swarmctl volume <create|list|inspect|rm> ...")
		os.Exit(1)
	}

	volumesDir := "/var/lib/swarmcracker/volumes"
	if envState := os.Getenv("SWARM_STATE_DIR"); envState != "" {
		volumesDir = filepath.Join(envState, "volumes")
	}

	switch args[0] {
	case "create":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl volume create <name> [--size MB]")
			os.Exit(1)
		}
		name := args[1]
		sizeMB := 0
		for i := 2; i < len(args); i++ {
			if args[i] == "--size" && i+1 < len(args) {
				sizeMB, _ = strconv.Atoi(args[i+1])
			}
		}
		volPath := filepath.Join(volumesDir, name)
		if err := os.MkdirAll(volPath, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create volume: %v\n", err)
			os.Exit(1)
		}
		// Write metadata
		meta := map[string]string{"name": name, "path": volPath, "size_mb": fmt.Sprintf("%d", sizeMB)}
		metaJSON, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(volPath, "meta.json"), metaJSON, 0644)
		fmt.Printf("Volume %s created at %s\n", name, volPath)

	case "list", "ls":
		if _, err := os.Stat(volumesDir); os.IsNotExist(err) {
			fmt.Println("No volumes directory")
			return
		}
		entries, err := os.ReadDir(volumesDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list volumes: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%-20s %-40s %-10s\n", "NAME", "PATH", "SIZE")
		for _, entry := range entries {
			if entry.IsDir() {
				volPath := filepath.Join(volumesDir, entry.Name())
				metaPath := filepath.Join(volPath, "meta.json")
				size := "-"
				if meta, err := os.ReadFile(metaPath); err == nil {
					var m map[string]string
					json.Unmarshal(meta, &m)
					if s, ok := m["size_mb"]; ok && s != "0" {
						size = s + "MB"
					}
				}
				fmt.Printf("%-20s %-40s %-10s\n", entry.Name(), volPath, size)
			}
		}

	case "inspect":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl volume inspect <name>")
			os.Exit(1)
		}
		name := args[1]
		volPath := filepath.Join(volumesDir, name)
		metaPath := filepath.Join(volPath, "meta.json")
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Volume %s not found\n", name)
			os.Exit(1)
		}
		meta, err := os.ReadFile(metaPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read metadata: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(meta))

	case "rm", "remove":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl volume rm <name>")
			os.Exit(1)
		}
		name := args[1]
		volPath := filepath.Join(volumesDir, name)
		if err := os.RemoveAll(volPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to remove volume: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Volume %s removed\n", name)

	default:
		fmt.Printf("Unknown volume command: %s\n", args[0])
		fmt.Println("Available: create, list, inspect, rm")
		os.Exit(1)
	}
}
