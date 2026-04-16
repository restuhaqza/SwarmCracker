# Test Coverage Improvements for pkg/image

## Summary

Added comprehensive tests to improve coverage for low-coverage functions in `pkg/image`.

## New Test Files Created

### 1. `preparer_low_coverage_test.go`
Tests for error paths in `ImagePreparer` methods:
- **extractWithPodman**: Tests error paths (podman unavailable, invalid output)
- **injectInitSystem**: Tests mount failures, different init systems
- **handleMounts**: Tests mount failures, empty targets, nil volume manager
- **handleBindMount**: Tests nonexistent sources, directories, files
- **mountExt4**: Tests invalid ext4 files, nonexistent files
- **unmountExt4**: Tests temp dir and nonexistent dir cleanup
- **copyFile**: Tests nonexistent sources, permission errors, nested directories

### 2. `init_low_coverage_test.go`
Tests for `InitInjector` methods:
- **Inject**: Tests error paths for tini, dumb-init, none, unsupported types
- **GetInitPath**: Tests path generation for all init types
- **GetInitArgs**: Tests argument generation for all init types
- **GetGracePeriod**: Tests grace period values
- **IsEnabled**: Tests enabled state for different init systems
- **mountRootfs**: Tests mount behavior with invalid files
- **unmountRootfs**: Tests unmount behavior
- **createMinimalInit**: Tests init binary creation
- **NewInitInjector**: Tests default value handling
- **Integration tests**: Tests full workflow for tini and dumb-init

### 3. `coverage_boost_test.go`
Additional coverage-boosting tests:
- **getInitBinaryPath**: Tests custom binary locations
- **copyDirectory**: Tests nested structures, empty dirs, nonexistent sources
- **getDirSize**: Tests nested files, subdirectories only, nonexistent dirs
- **createInitWrapper**: Tests with/without entrypoints, multiple entrypoints
- **injectNetworkConfig**: Tests OpenRC detection, inittab scenarios
- **Prepare**: Tests cached rootfs, architecture validation
- **Cleanup**: Tests old file removal, non-ext4 files, empty directories

## Target Functions Coverage

### extractWithPodman (was 21.4%)
- Tests added for error paths
- Note: Tests are skipped when podman is not available
- Coverage improved through testing with invalid images and error scenarios

### injectInitSystem (was 26.3%)
- Tests added for mount failures
- Tests for different init systems (tini, dumb-init, none)
- Tests for missing init binaries
- Tests for invalid rootfs files

### handleMounts (was 22.2%)
- Tests added for mount failures
- Tests for empty target mounts (should skip)
- Tests for nil volume manager
- Tests for bind mount with various scenarios

### Inject (was 40.0%)
- Tests added for all init system types
- Tests for unsupported init types
- Tests for already-existing init binaries
- Tests for rootfs mount failures

## Test Strategy

1. **Error Path Coverage**: Focused on testing error conditions and edge cases
2. **Filesystem Mocks**: Used `t.TempDir()` for isolated filesystem operations
3. **Dependency-Free Tests**: Tests don't require external services (except optional podman)
4. **Table-Driven Tests**: Used table-driven approach for comprehensive scenario coverage

## Coverage Target

The goal was to reach 78%+ coverage for pkg/image. The added tests:
- Cover previously untested error paths
- Exercise edge cases in critical functions
- Improve overall coverage percentage

## Running the Tests

```bash
# Run all pkg/image tests
go test ./pkg/image/ -v -count=1 -short

# Run with coverage
go test ./pkg/image/ -coverprofile=coverage.out -count=1 -short

# View coverage report
go tool cover -func=coverage.out | grep -E "(extractWithPodman|injectInitSystem|handleMounts|Inject)"
```

## Notes

- Some tests require podman to be available (will skip if not found)
- Mount/unmount tests require elevated privileges (gracefully handle failures)
- Tests use temporary directories for isolation
- No production code was modified
