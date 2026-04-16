# SwarmCracker TODO Implementation Plan

## Status: ALL PHASES COMPLETE ✅

---

9 TODO items found across production code and tests. This plan prioritizes production code by complexity.

---

## Phase 1: Quick Wins (LOW Complexity)

### 1.1 Fix HT/SMT Extraction
**File:** `pkg/swarmkit/vmm.go:296`
**Issue:** `HtEnabled` hardcoded to `false`, should extract from machine config

**Current:**
```go
HtEnabled:  false, // TODO: Extract from machine config
```

**Fix:**
```go
HtEnabled:  toBool(machineConfig["smt"]), // Extract from machine-config
```

**Add helper:**
```go
func toBool(v interface{}) bool {
    if v == nil {
        return false
    }
    if b, ok := v.(bool); ok {
        return b
    }
    return false
}
```

**Effort:** 10 minutes
**Impact:** Correctness - enables SMT when configured

---

### 1.2 YAML Config Loading
**File:** `cmd/swarmcracker-agent/main.go:75`
**Issue:** Hardcoded default config instead of YAML loading

**Current:**
```go
// TODO: Implement YAML config loading
return &swarmkit.Config{...}
```

**Fix:**
```go
import "gopkg.in/yaml.v3"

func loadConfig(path string) (*swarmkit.Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("failed to read config: %w", err)
    }
    
    config := &swarmkit.Config{}
    if err := yaml.Unmarshal(data, config); err != nil {
        return nil, fmt.Errorf("failed to parse config: %w", err)
    }
    
    // Set defaults for missing fields
    setConfigDefaults(config)
    
    return config, nil
}

func setConfigDefaults(c *swarmkit.Config) {
    if c.FirecrackerPath == "" {
        c.FirecrackerPath = "firecracker"
    }
    if c.KernelPath == "" {
        c.KernelPath = "/usr/share/firecracker/vmlinux"
    }
    // ... other defaults
}
```

**Effort:** 30 minutes
**Impact:** Essential - enables proper configuration

---

## Phase 2: Medium Complexity

### 2.1 IO Device Discovery for Cgroups
**File:** `pkg/jailer/cgroup.go:305`
**Issue:** IO bandwidth limits not applied (need device major:minor)

**Approach:**
1. Discover block devices from rootfs path
2. Get major:minor numbers via `/sys/dev/block/`
3. Write to `io.max` file

**Implementation:**
```go
func (m *CgroupManager) setIODeviceLimits(cgroupPath string, limits ResourceLimits) error {
    if limits.IOReadBPS == 0 && limits.IOWriteBPS == 0 {
        return nil
    }
    
    // Discover the block device backing the rootfs
    devices, err := m.discoverBlockDevices()
    if err != nil {
        return fmt.Errorf("failed to discover block devices: %w", err)
    }
    
    ioMaxPath := filepath.Join(cgroupPath, "io.max")
    for _, dev := range devices {
        line := fmt.Sprintf("%d:%d rbps=%d wbps=%d", 
            dev.Major, dev.Minor, limits.IOReadBPS, limits.IOWriteBPS)
        if err := os.WriteFile(ioMaxPath, []byte(line), 0644); err != nil {
            return fmt.Errorf("failed to write io.max: %w", err)
        }
    }
    return nil
}

func (m *CgroupManager) discoverBlockDevices() ([]BlockDevice, error) {
    // Parse /proc/mounts or use stat on rootfs to get device
    // Extract major:minor from /sys/dev/block/
}
```

**Effort:** 1-2 hours
**Impact:** Correctness - enables IO throttling

---

### 2.2 Network Key Management
**File:** `pkg/swarmkit/executor.go:332`
**Issue:** `SetNetworkBootstrapKeys` does nothing

**Approach:**
1. Store keys in secure memory/disk
2. Use keys for VXLAN encryption
3. Integrate with VXLANManager

**Implementation:**
```go
func (e *Executor) SetNetworkBootstrapKeys(keys []*api.EncryptionKey) error {
    if len(keys) == 0 {
        return nil
    }
    
    // Store keys securely
    e.networkKeys = keys
    
    // Pass to network manager if available
    if e.networkMgr != nil {
        return e.networkMgr.SetEncryptionKeys(keys)
    }
    
    return nil
}

// In NetworkManager:
func (nm *NetworkManager) SetEncryptionKeys(keys []*api.EncryptionKey) error {
    // Configure VXLAN with encryption keys
    // Keys used for secure peer communication
}
```

**Effort:** 2-3 hours
**Impact:** Security - enables encrypted overlay network

---

### 2.3 SwarmKit Node Discovery
**File:** `pkg/network/manager.go:703`
**Issue:** Peer discovery hardcoded/empty

**Approach:**
1. Query SwarmKit for active nodes
2. Filter by VXLAN-enabled nodes
3. Return peer IPs

**Implementation:**
```go
func (nm *NetworkManager) discoverPeerWorkers() []string {
    if nm.nodeDiscovery == nil {
        return []string{}
    }
    
    nodes, err := nm.nodeDiscovery.GetNodes()
    if err != nil {
        nm.logger.Warn().Err(err).Msg("Failed to discover nodes")
        return []string{}
    }
    
    peers := []string{}
    for _, node := range nodes {
        if node.Status == api.NodeStatus_READY {
            // Extract VXLAN IP from node
            peers = append(peers, node.VXLANIP)
        }
    }
    return peers
}

// Add interface:
type NodeDiscovery interface {
    GetNodes() ([]NodeInfo, error)
}
```

**Effort:** 2-3 hours
**Impact:** Integration - automatic peer discovery

---

## Phase 3: High Complexity (Full Features)

### 3.1 Local Rootfs Preparation
**File:** `cmd/swarmcracker/main.go:690`
**Issue:** Rootfs not prepared locally before deploy

**Full Workflow:**
1. Pull OCI image (containerd/docker)
2. Extract filesystem to temp dir
3. Create ext4 rootfs image
4. Upload to remote hosts
5. Clean up temp files

**Implementation:**
```go
func prepareLocalRootfs(imageRef string, outputDir string) (string, error) {
    // 1. Pull image using containerd or skopeo
    imageDir := filepath.Join(outputDir, "image-extract")
    if err := pullAndExtractImage(imageRef, imageDir); err != nil {
        return "", err
    }
    
    // 2. Create ext4 rootfs
    rootfsPath := filepath.Join(outputDir, "rootfs.ext4")
    if err := createExt4Rootfs(imageDir, rootfsPath, 512); err != nil {
        return "", err
    }
    
    // 3. Return path for upload
    return rootfsPath, nil
}

func createExt4Rootfs(sourceDir string, outputPath string, sizeMB int) error {
    // Create raw image file
    // Format as ext4
    // Mount and copy files
    // Unmount
}
```

**Dependencies:**
- `guestfs` or `mkfs.ext4` + mount commands
- containerd client or skopeo binary

**Effort:** 4-6 hours
**Impact:** Feature - enables full deployment workflow

---

### 3.2 Full Deployment Logic (Bash Script)
**File:** `cmd/swarmcracker/main.go:942`
**Issue:** Deployment script incomplete

**Current placeholder, needs:**
1. Pull OCI image
2. Create rootfs
3. Setup network (TAP/bridge)
4. Start Firecracker VM
5. Configure VM via API

**Implementation:** Convert to Go code in `deployToHost()` instead of bash script:
```go
func deployToHost(host string, plan *DeploymentPlan) error {
    client, err := createSSHClient(...)
    
    // 1. Upload rootfs
    err = uploadRootfs(client, localRootfsPath, remoteRootfsPath)
    
    // 2. Setup network on remote
    err = setupRemoteNetwork(client, plan)
    
    // 3. Start Firecracker
    err = startFirecrackerVM(client, taskID, plan)
    
    // 4. Configure via API
    err = configureVM(client, taskID, plan)
    
    return nil
}
```

**Effort:** 4-6 hours
**Impact:** Feature - completes deployment workflow

---

## Phase 4: Test TODOs (Optional)

### 4.1 Network Connectivity Test
**File:** `test/e2e/full_workflow_test.go:246`
**Effort:** 2 hours
**Deferred:** Test enhancement, not production blocker

### 4.2 Network Setup Test
**File:** `test/e2e/firecracker/04_real_image_test.go:187`
**Effort:** 2 hours
**Deferred:** Test enhancement, not production blocker

---

## Recommended Execution Order

| Phase | Task | Effort | Priority |
|-------|------|--------|----------|
| 1.1 | HT/SMT extraction | 10 min | 🔴 HIGH (correctness) |
| 1.2 | YAML config loading | 30 min | 🔴 HIGH (essential) |
| 2.1 | IO device discovery | 1-2 hr | 🟡 MEDIUM (feature) |
| 2.2 | Network key management | 2-3 hr | 🟡 MEDIUM (security) |
| 2.3 | SwarmKit node discovery | 2-3 hr | 🟡 MEDIUM (integration) |
| 3.1 | Local rootfs preparation | 4-6 hr | 🟢 LOW (future feature) |
| 3.2 | Full deployment logic | 4-6 hr | 🟢 LOW (future feature) |

**Total Quick Wins:** 40 minutes
**Total Medium:** 5-8 hours
**Total High:** 8-12 hours

---

## Next Action

Start with **Phase 1** (quick wins):
1. Fix HT/SMT extraction (10 min)
2. Implement YAML config loading (30 min)

Then proceed to Phase 2 based on priorities.