package golden

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Artifact is a built golden image resolved from a store directory.
type Artifact struct {
	// Ref is the canonical "name@version" reference.
	Ref string
	// Name and Version identify the recipe.
	Name    string
	Version string
	// Path is the ext4 artifact on the host.
	Path string
	// Metadata is the artifact sidecar.
	Metadata *ArtifactMetadata
}

// Resolve finds a built artifact in storeDir by "name" (newest version) or
// "name@version". It verifies both the metadata sidecar and the artifact exist.
func Resolve(storeDir, ref string) (*Artifact, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("golden: empty image reference")
	}

	name, version := ref, ""
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		name, version = ref[:i], ref[i+1:]
	}
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("golden: invalid image name %q", name)
	}
	if version != "" && !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("golden: invalid version %q", version)
	}

	var metadataPath string
	if version != "" {
		metadataPath = filepath.Join(storeDir, ArtifactBaseName(name, version)+".json")
		if _, err := os.Stat(metadataPath); err != nil {
			return nil, fmt.Errorf("golden: image %s@%s not found in %s", name, version, storeDir)
		}
	} else {
		matches, err := filepath.Glob(filepath.Join(storeDir, "golden-"+name+"-*.json"))
		if err != nil {
			return nil, fmt.Errorf("golden: glob %s: %w", storeDir, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("golden: image %s not found in %s", name, storeDir)
		}
		sort.Slice(matches, func(i, j int) bool {
			fi, errI := os.Stat(matches[i])
			fj, errJ := os.Stat(matches[j])
			if errI != nil || errJ != nil {
				return matches[i] < matches[j]
			}
			return fi.ModTime().After(fj.ModTime())
		})
		metadataPath = matches[0]
	}

	md, err := ReadMetadata(metadataPath)
	if err != nil {
		return nil, err
	}

	artifactPath := filepath.Join(storeDir, md.Artifact)
	if info, err := os.Stat(artifactPath); err != nil {
		return nil, fmt.Errorf("golden: artifact %s for %s@%s is missing: %w", artifactPath, md.Name, md.Version, err)
	} else if info.Size() == 0 {
		return nil, fmt.Errorf("golden: artifact %s for %s@%s is empty", artifactPath, md.Name, md.Version)
	}

	return &Artifact{
		Ref:      md.Name + "@" + md.Version,
		Name:     md.Name,
		Version:  md.Version,
		Path:     artifactPath,
		Metadata: md,
	}, nil
}
