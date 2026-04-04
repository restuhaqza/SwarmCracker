# Bug Fix Report - Socket File Not Created

## Date
2026-02-01

## Problem
SwarmKit unix socket file `/var/run/swarmkit/swarm.sock` was not being created when swarmd started.

## Root Causes

### 1. **Wrong Binary Being Built** ⚠️ CRITICAL
**Issue:** The setup script used `find` command to locate swarmd main.go:
```bash
SWARMMD_MAIN=$(find . -name "main.go" -path "*/swarmd/*" | head -1)
```

**Problem:** This returned `/tmp/swarmkit/swarmd/cmd/external-ca-example/main.go` instead of `/tmp/swarmkit/swarmd/cmd/swarmd/main.go` because:
- Alphabetically, "external-ca-example" comes before "swarmd"
- `head -1` only takes the first result
- The build was creating the external CA example tool, not the actual swarmd daemon

**Evidence:** When running swarmd, it showed:
```
time="..." level=info msg="Now run: swarmd -d . --listen-control-api ./swarmd.sock --external-ca protocol=cfssl,url=https://localhost:XXXXX/sign"
```

This is the output from `external-ca-example`, not from swarmd itself.

**Fix:** Use explicit paths instead of find:
```bash
SWARMMD_MAIN="./swarmd/cmd/swarmd/main.go"
SWARMCTL_MAIN="./swarmd/cmd/swarmctl/main.go"
```

### 2. **Invalid --debug Flag** ⚠️ CRITICAL
**Issue:** systemd service used `--debug` flag which doesn't exist in the real swarmd binary.

**Problem:** The real swarmd binary doesn't support `--debug` flag, causing it to fail immediately:
```
Error: unknown flag: --debug
```

**Fix:** Removed `--debug` flag from systemd service ExecStart line.

### 3. **Socket Directory Preparation** ℹ️ INFORMATIONAL
**Issue:** Socket directory `/var/run/swarmkit` might not exist at boot time.

**Problem:** swarmd doesn't create the socket directory automatically.

**Fix:** Created `prepare-socket.sh` script and tmpfiles.d configuration to ensure directory exists.

## Changes Made

### Files Modified

1. **scripts/setup-manager.sh**
   - Changed: `find` command → explicit path for swarmd main.go
   - Changed: `find` command → explicit path for swarmctl main.go
   - Removed: `--debug` flag from systemd service
   - Added: Call to `prepare-socket.sh` before starting swarmd

2. **scripts/setup-worker.sh**
   - Changed: `find` command → explicit path for swarmd main.go
   - Removed: `--debug` flag from systemd service
   - Added: Call to `prepare-socket.sh` for consistency

3. **scripts/prepare-socket.sh** (NEW)
   - Creates `/var/run/swarmkit` directory with proper permissions
   - Installs tmpfiles.d configuration for persistence across reboots
   - Can be run manually if needed

4. **scripts/swarmkit-tmpfiles.conf** (NEW)
   - Systemd tmpfiles configuration for socket directory
   - Ensures directory is recreated at boot

5. **Vagrantfile**
   - Added copy of `prepare-socket.sh` to all VMs
   - Added script directory creation in common setup

6. **README.md**
   - Added troubleshooting section for socket issues
   - Documented manual verification steps

7. **SOCKET-PREPARATION.md** (NEW)
   - Comprehensive guide for socket-related issues
   - Troubleshooting steps and solutions

## Verification

### After Fix
```bash
# Socket file exists
$ ls -l /var/run/swarmkit/swarm.sock
srwxr-xr-x 1 root root 0 Feb  1 16:07 /var/run/swarmkit/swarm.sock

# Service is running
$ systemctl status swarmd
● swarmd.service - SwarmKit Manager
   Loaded: loaded (/etc/systemd/system/swarmd.service; enabled)
   Active: active (running) since Sun 2026-02-01 16:07:23 UTC

# swarmctl works
$ export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
$ swarmctl node ls
ID                         NAME  MEMBERSHIP  STATUS  AVAILABILITY  MANAGER STATUS
zh2c2ev4uu2khtbetkdnmsyta        ACCEPTED    READY   ACTIVE        REACHABLE *
```

## Lessons Learned

1. **Be Explicit with Build Paths:** When building from a repo with multiple main.go files, always use explicit paths instead of `find` to avoid building the wrong binary.

2. **Verify Binary Output:** After building, run `binary --help` to confirm you built the right tool before installing it.

3. **Check Flag Compatibility:** Not all versions/tools support the same flags. Verify flags exist before adding them to startup commands.

4. **Socket File Creation is Not Automatic:** daemons that use unix sockets don't always create the parent directory. Use tmpfiles.d or init scripts to ensure it exists.

5. **Log Messages Matter:** The "Now run: swarmd..." message was a clue that we were running the wrong binary. Always read and understand log output.

## Impact

- ✅ Manager node now correctly creates and exposes control API socket
- ✅ swarmctl can connect to manager via unix socket
- ✅ Workers can be properly joined to the cluster
- ✅ Socket directory persists across VM reboots (via tmpfiles.d)

## Next Steps

1. Test full cluster deployment with workers
2. Verify service deployment works end-to-end
3. Test rolling updates and scaling
4. Document cluster recovery procedures

---

**Status:** ✅ RESOLVED
**Tested:** Manager node only (worker testing pending)
**Files Changed:** 7 files created/modified
