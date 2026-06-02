package logging

import (
	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
)

// ZerologHook forwards logrus log entries to zerolog.
// This ensures SwarmKit's internal logrus output is consistent
// with SwarmCracker's zerolog-structured logging.
type ZerologHook struct {
	Logger zerolog.Logger
}

// Fire is called by logrus for each log entry.
func (h *ZerologHook) Fire(entry *logrus.Entry) error {
	var ev *zerolog.Event

	switch entry.Level {
	case logrus.DebugLevel, logrus.TraceLevel:
		ev = h.Logger.Debug()
	case logrus.InfoLevel:
		ev = h.Logger.Info()
	case logrus.WarnLevel:
		ev = h.Logger.Warn()
	case logrus.ErrorLevel:
		ev = h.Logger.Error()
	case logrus.FatalLevel:
		ev = h.Logger.Fatal()
	case logrus.PanicLevel:
		ev = h.Logger.Panic()
	default:
		ev = h.Logger.Info()
	}

	// Add logrus fields to the zerolog event
	for k, v := range entry.Data {
		ev = ev.Interface(k, v)
	}

	ev.Msg(entry.Message)
	return nil
}

// Levels returns all log levels — we want every logrus message forwarded.
func (h *ZerologHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

// InstallZerologHook configures logrus to forward all messages to zerolog.
// Call this once during startup. After this, all logrus messages
// (including from SwarmKit internals) will appear in zerolog format.
func InstallZerologHook(logger zerolog.Logger) {
	hook := &ZerologHook{Logger: logger}
	logrus.AddHook(hook)
	logrus.SetFormatter(&logrus.TextFormatter{
		DisableTimestamp: true, // zerolog handles timestamps
		DisableColors:    true,
	})
}
