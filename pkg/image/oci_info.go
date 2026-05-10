// Package image provides OCI image configuration parsing.
package image

import v1 "github.com/google/go-containerregistry/pkg/v1"

// OCIImageInfo holds parsed OCI image configuration.
type OCIImageInfo struct {
	Entrypoint   []string // OCI ENTRYPOINT (exec form)
	Cmd          []string // OCI CMD (exec form)
	Env          []string // KEY=VALUE pairs
	User         string   // OCI USER (e.g. "nginx", "1000:1000")
	WorkDir      string   // OCI WORKDIR
	StopSignal   string   // OCI STOPSIGNAL (default "SIGTERM")
	OS           string   // Image OS (e.g. "linux")
	Architecture string   // Image architecture (e.g. "amd64")
	ImageRef     string   // Original image reference (e.g. "nginx:latest")
}

// DefaultStopSignal is the default signal sent to stop a container.
const DefaultStopSignal = "SIGTERM"

// ParseOCIImageConfig parses a go-containerregistry ConfigFile into OCIImageInfo.
func ParseOCIImageConfig(cfg *v1.ConfigFile, imageRef string) *OCIImageInfo {
	if cfg == nil {
		return &OCIImageInfo{
			StopSignal: DefaultStopSignal,
			ImageRef:   imageRef,
		}
	}

	info := &OCIImageInfo{
		ImageRef:     imageRef,
		Entrypoint:   cfg.Config.Entrypoint,
		Cmd:          cfg.Config.Cmd,
		Env:          cfg.Config.Env,
		User:         cfg.Config.User,
		WorkDir:      cfg.Config.WorkingDir,
		StopSignal:   DefaultStopSignal,
		OS:           cfg.OS,
		Architecture: cfg.Architecture,
	}

	// Parse StopSignal from config if present
	if cfg.Config.StopSignal != "" {
		info.StopSignal = cfg.Config.StopSignal
	}

	return info
}

// FullCommand returns the combined command to execute.
// OCI spec: ENTRYPOINT defines the executable, CMD defines default arguments.
// Rules:
//   - ENTRYPOINT only → use ENTRYPOINT
//   - CMD only → use CMD
//   - Both → ENTRYPOINT + CMD
//   - Neither → ["/bin/sh"]
func FullCommand(info *OCIImageInfo) []string {
	if info == nil {
		return []string{"/bin/sh"}
	}

	// Both ENTRYPOINT and CMD: combine them (entrypoint + cmd as args)
	if len(info.Entrypoint) > 0 && len(info.Cmd) > 0 {
		result := make([]string, 0, len(info.Entrypoint)+len(info.Cmd))
		result = append(result, info.Entrypoint...)
		result = append(result, info.Cmd...)
		return result
	}

	// ENTRYPOINT only
	if len(info.Entrypoint) > 0 {
		return info.Entrypoint
	}

	// CMD only
	if len(info.Cmd) > 0 {
		return info.Cmd
	}

	// Neither: default shell
	return []string{"/bin/sh"}
}

// HasEntrypoint returns true if the image defines an ENTRYPOINT.
func (info *OCIImageInfo) HasEntrypoint() bool {
	return info != nil && len(info.Entrypoint) > 0
}

// HasCmd returns true if the image defines a CMD.
func (info *OCIImageInfo) HasCmd() bool {
	return info != nil && len(info.Cmd) > 0
}

// IsEmpty returns true if the OCI info has no meaningful configuration.
func (info *OCIImageInfo) IsEmpty() bool {
	return info == nil || (len(info.Entrypoint) == 0 && len(info.Cmd) == 0 && len(info.Env) == 0)
}