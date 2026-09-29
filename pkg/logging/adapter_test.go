package logging

import (
	"bytes"
	"io"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestZerologHook_Fire_AllLevels covers every non-lethal level branch in Fire.
//
// FatalLevel (os.Exit) and PanicLevel (panic) are deliberately excluded from the
// table because invoking them in-process would kill the test binary. The default
// branch is exercised with an out-of-range level.
func TestZerologHook_Fire_AllLevels(t *testing.T) {
	levels := []logrus.Level{
		logrus.DebugLevel,
		logrus.TraceLevel,
		logrus.InfoLevel,
		logrus.WarnLevel,
		logrus.ErrorLevel,
		logrus.Level(99), // out of range -> default branch
	}

	for _, level := range levels {
		t.Run(level.String(), func(t *testing.T) {
			var buf bytes.Buffer
			hook := &ZerologHook{Logger: zerolog.New(&buf)}

			entry := &logrus.Entry{
				Level:   level,
				Message: "hello from logrus",
				Data:    logrus.Fields{"component": "test-hook"},
			}

			err := hook.Fire(entry)
			require.NoError(t, err)

			out := buf.String()
			assert.Contains(t, out, "hello from logrus", "message should be forwarded to zerolog")
			assert.Contains(t, out, "component", "structured field key should be forwarded")
			assert.Contains(t, out, "test-hook", "structured field value should be forwarded")
		})
	}
}

// TestZerologHook_Fire_PanicLevel exercises the Panic branch safely by
// recovering the panic rather than letting it abort the process.
func TestZerologHook_Fire_PanicLevel(t *testing.T) {
	var buf bytes.Buffer
	hook := &ZerologHook{Logger: zerolog.New(&buf)}

	entry := &logrus.Entry{
		Level:   logrus.PanicLevel,
		Message: "panicky message",
	}

	assert.Panics(t, func() {
		_ = hook.Fire(entry)
	})
	assert.Contains(t, buf.String(), "panicky message")
}

func TestZerologHook_Levels(t *testing.T) {
	hook := &ZerologHook{Logger: zerolog.Nop()}
	assert.Equal(t, logrus.AllLevels, hook.Levels())
}

func TestInstallZerologHook(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)

	oldOut := logrus.StandardLogger().Out
	logrus.SetOutput(io.Discard)
	t.Cleanup(func() { logrus.SetOutput(oldOut) })

	require.NotPanics(t, func() {
		InstallZerologHook(logger)
	})

	logrus.Info("forwarded from logrus")

	assert.Contains(t, buf.String(), "forwarded from logrus",
		"logrus messages should be forwarded through the installed zerolog hook")
}
