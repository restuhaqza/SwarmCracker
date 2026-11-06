package golden

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plant writes files and symlinks into a rootfs tree. Names are guest paths, so
// a test reads like the image it describes.
func plant(t *testing.T, root string, files map[string]string, links map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	for name, target := range links {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.Symlink(target, path))
	}
}

// dirtyImage is a rootfs that looks like a machine that ran: an identity, host
// keys, history, logs, and the runtime state of a started service — plus two
// files a seal must not touch.
func dirtyImage(t *testing.T, root string) {
	t.Helper()
	plant(t, root, map[string]string{
		"/etc/machine-id":                          "9f3c1b6e4a2d4f0b8c7e5a1d2f3b4c5d\n",
		"/etc/ssh/ssh_host_ed25519_key":            "-----BEGIN OPENSSH PRIVATE KEY-----\n",
		"/etc/ssh/ssh_host_ed25519_key.pub":        "ssh-ed25519 AAAA\n",
		"/root/.bash_history":                      "apk add --no-cache docker\n",
		"/home/dev/.bash_history":                  "git clone https://example.invalid/x\n",
		"/var/log/syslog":                          "Nov 6 12:00:00 builder kernel: booted\n",
		"/var/log/journal/9f3c1b6e/system.journal": "journal bytes",
		"/run/sc-agent.pid":                        "412\n",
		"/run/lock/swarmcracker.lock":              "",
		// Not the seal's business: the identity the operator chose, and a file
		// whose name only looks like the ones above.
		"/etc/hostname":       "builder\n",
		"/root/.bashrc":       "export PS1='$ '\n",
		"/etc/machine-id.txt": "not the machine id\n",
	}, map[string]string{
		"/var/lib/dbus/machine-id": "/etc/machine-id",
	})
}

// TestSealTemplate_CleansWhatAMachineLeftBehind is the whole point of the
// layer: after the seal, the image carries no identity of the machine that
// built it, and the build's own proof of that is verifySealed.
func TestSealTemplate_CleansWhatAMachineLeftBehind(t *testing.T) {
	root := t.TempDir()
	dirtyImage(t, root)

	require.NoError(t, sealTemplate(root))
	require.NoError(t, verifySealed(root), "the seal must leave the image verifiably clean")

	// The identity is emptied, not deleted: distributions' own tooling and
	// systemd expect the file to exist and read an empty one as "no identity
	// yet", which is what makes the first boot generate one.
	info, err := os.Stat(filepath.Join(root, "/etc/machine-id"))
	require.NoError(t, err)
	assert.Zero(t, info.Size(), "an empty /etc/machine-id is what makes the guest generate its own")

	// The D-Bus copy is a link to it, so emptying one is emptying both.
	target, err := resolveLink(root, filepath.Join(root, "/var/lib/dbus/machine-id"))
	require.NoError(t, err)
	assert.Equal(t, "/etc/machine-id", target)

	hostKeys, err := filepath.Glob(filepath.Join(root, sshHostKeyGlob))
	require.NoError(t, err)
	assert.Empty(t, hostKeys, "no host key may survive, private or public")

	for _, gone := range []string{"/root/.bash_history", "/home/dev/.bash_history"} {
		_, err := os.Lstat(filepath.Join(root, gone))
		assert.ErrorIs(t, err, os.ErrNotExist, "%s must be gone", gone)
	}

	// Logs are emptied, not removed: a service that expects its log file still
	// finds one.
	for _, emptied := range []string{"/var/log/syslog", "/var/log/journal/9f3c1b6e/system.journal"} {
		info, err := os.Stat(filepath.Join(root, emptied))
		require.NoError(t, err, "%s must still exist", emptied)
		assert.Zero(t, info.Size(), "%s must be empty", emptied)
	}

	// Nothing in the image claims a service is already running.
	entries, err := os.ReadDir(filepath.Join(root, runDir))
	require.NoError(t, err)
	assert.Empty(t, entries, "/run must be empty")

	// And the seal is scoped: what it is not asked to clean is untouched.
	for name, want := range map[string]string{
		"/etc/hostname":       "builder\n",
		"/root/.bashrc":       "export PS1='$ '\n",
		"/etc/machine-id.txt": "not the machine id\n",
	} {
		got, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		assert.Equal(t, want, string(got), "%s is not a seal step's business", name)
	}
}

// TestVerifySealed_RefusesWhatTheSealCannotClean is what makes the seal a
// guarantee instead of a script: each of these images is what a build would
// record if the seal had only *tried*, and each must fail the build instead.
func TestVerifySealed_RefusesWhatTheSealCannotClean(t *testing.T) {
	cases := map[string]struct {
		plant func(t *testing.T, root string)
		want  string
	}{
		"a machine identity the seal did not reach": {
			plant: func(t *testing.T, root string) {
				plant(t, root, map[string]string{"/etc/machine-id": "9f3c1b6e4a2d4f0b8c7e5a1d2f3b4c5d\n"}, nil)
			},
			want: "/etc/machine-id carries a machine identity",
		},
		"a machine identity behind a link out of the image": {
			plant: func(t *testing.T, root string) {
				plant(t, root, map[string]string{"/tmp/identity": "9f3c1b6e\n"},
					map[string]string{"/var/lib/dbus/machine-id": "/tmp/identity"})
			},
			want: "/var/lib/dbus/machine-id carries a machine identity",
		},
		"an SSH host key": {
			plant: func(t *testing.T, root string) {
				plant(t, root, map[string]string{"/etc/ssh/ssh_host_rsa_key": "key\n"}, nil)
			},
			want: "/etc/ssh/ssh_host_rsa_key is an SSH host key",
		},
		"shell history": {
			plant: func(t *testing.T, root string) {
				plant(t, root, map[string]string{"/home/dev/.bash_history": "history\n"}, nil)
			},
			want: "/home/dev/.bash_history holds shell history",
		},
		"a log of the build": {
			plant: func(t *testing.T, root string) {
				plant(t, root, map[string]string{"/var/log/apt/term.log": "log\n"}, nil)
			},
			want: "/var/log/apt/term.log holds a log",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			tc.plant(t, root)

			err := verifySealed(root)
			require.Error(t, err, "an image that is not clean must not pass the seal")
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("a clean image passes", func(t *testing.T) {
		assert.NoError(t, verifySealed(t.TempDir()))
	})

	t.Run("an image the seal already cleaned passes", func(t *testing.T) {
		root := t.TempDir()
		dirtyImage(t, root)
		require.NoError(t, sealTemplate(root))
		assert.NoError(t, verifySealed(root))
	})
}

// TestBuilder_Build_PacksASealedImage holds the seal to the packer's own view:
// what the ext4 builder is handed has already been sealed, so nothing is emptied
// after the image exists to be emptied in.
func TestBuilder_Build_PacksASealedImage(t *testing.T) {
	h := newHarness(t, false)
	h.extract.plant = func(dest string) error {
		dirtyImage(t, dest)
		return nil
	}

	packed := map[string]int64{}
	h.ext4.inspect = func(dir string) {
		for _, name := range []string{"/etc/machine-id", "/var/log/syslog"} {
			info, err := os.Stat(filepath.Join(dir, name))
			if err == nil {
				packed[name] = info.Size()
			}
		}
		hostKeys, _ := filepath.Glob(filepath.Join(dir, sshHostKeyGlob))
		packed["host keys"] = int64(len(hostKeys))
		entries, _ := os.ReadDir(filepath.Join(dir, runDir))
		packed["/run"] = int64(len(entries))
	}

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.NoError(t, err)

	assert.Equal(t, map[string]int64{
		"/etc/machine-id": 0,
		"/var/log/syslog": 0,
		"host keys":       0,
		"/run":            0,
	}, packed)
	assert.Contains(t, h.calls, "probe", "the sealed image is still the one that is booted")
}

// TestBuilder_Build_FailsWhenTheImageCannotBeSealed is the rule that makes the
// seal worth anything: an image the seal cannot clean fails the build, and
// nothing is packed for a later resolve to find.
func TestBuilder_Build_FailsWhenTheImageCannotBeSealed(t *testing.T) {
	h := newHarness(t, false)
	h.extract.plant = func(dest string) error {
		plant(t, dest, map[string]string{"/tmp/identity": "9f3c1b6e\n"},
			map[string]string{"/var/lib/dbus/machine-id": "/tmp/identity"})
		return nil
	}

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/var/lib/dbus/machine-id carries a machine identity")
	assert.NotContains(t, h.calls, "ext4", "a failed seal must not pack an image")
	assert.NotContains(t, h.calls, "probe")

	files, err := filepath.Glob(filepath.Join(h.outDir, "*"))
	require.NoError(t, err)
	assert.Empty(t, files, "no artifact may be left behind")
}
