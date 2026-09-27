package cni

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteConfigFile writes a CNI configuration file to disk
func WriteConfigFile(configDir, name string, config []byte) error {
	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	filename := filepath.Join(configDir, name+".conf")
	return os.WriteFile(filename, config, 0600)
}
func RemoveConfigFile(configDir, name string) error {
	// Try .conf file first
	confPath := filepath.Join(configDir, name+".conf")
	if _, err := os.Stat(confPath); err == nil {
		if err := os.Remove(confPath); err != nil {
			return fmt.Errorf("failed to remove conf file: %w", err)
		}
		return nil
	}

	// Try .conflist file
	conflistPath := filepath.Join(configDir, name+".conflist")
	if _, err := os.Stat(conflistPath); err == nil {
		if err := os.Remove(conflistPath); err != nil {
			return fmt.Errorf("failed to remove conflist file: %w", err)
		}
		return nil
	}

	// Try numbered config files
	files, err := os.ReadDir(configDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read config directory: %w", err)
	}

	for _, file := range files {
		filename := file.Name()
		// Check if file contains network name
		if containsNetworkName(filename, name) {
			if err := os.Remove(filepath.Join(configDir, filename)); err != nil {
				return fmt.Errorf("failed to remove %s: %w", filename, err)
			}
		}
	}

	return nil
}

// validateCNINetworkName rejects network names that could escape the config
// directory or produce ambiguous config file names.
func validateCNINetworkName(name string) error {
	if name == "" {
		return fmt.Errorf("network name cannot be empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid network name %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid network name %q: must not contain path separators", name)
	}
	return nil
}

// isCNIConfigFile reports whether filename is a CNI config (.conf or .conflist).
func isCNIConfigFile(filename string) bool {
	return strings.HasSuffix(filename, ".conf") || strings.HasSuffix(filename, ".conflist")
}

// networkNameFromFile extracts the network name from a CNI config filename.
// Returns "" if the file is not a CNI config file.
func networkNameFromFile(filename string) string {
	if !isCNIConfigFile(filename) {
		return ""
	}
	base := filename
	if strings.HasSuffix(base, ".conflist") {
		base = strings.TrimSuffix(base, ".conflist")
	} else {
		base = strings.TrimSuffix(base, ".conf")
	}
	// Strip numeric prefix (e.g., "01-")
	if idx := findPrefixEnd(base); idx > 0 && idx < 4 {
		base = base[idx+1:]
	}
	return base
}

// containsNetworkName checks if a filename contains a network name
func containsNetworkName(filename, networkName string) bool {
	// Remove extension
	base := filename
	if idx := len(filename) - 5; idx > 0 && filename[idx:] == ".conf" {
		base = filename[:idx]
	} else if idx := len(filename) - 10; idx > 0 && filename[idx:] == ".conflist" {
		base = filename[:idx]
	}

	// Remove numeric prefix (e.g., "01-")
	if idx := findPrefixEnd(base); idx > 0 && idx < 4 {
		base = base[idx+1:]
	}

	return base == networkName
}

// findPrefixEnd finds the end of a numeric prefix
func findPrefixEnd(s string) int {
	for i, c := range s {
		if c < '0' || c > '9' {
			return i
		}
	}
	return 0
}
