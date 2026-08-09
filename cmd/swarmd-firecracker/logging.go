package main

import (
	"os"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/logging"
	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

func setupLogging(ctx *cli.Context) {
	// Setup SwarmKit logging (uses logrus)
	level := logrus.InfoLevel
	if ctx.Bool("debug") {
		level = logrus.DebugLevel
	}
	logrus.SetLevel(level)
	logrus.SetOutput(os.Stderr)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	// Forward all logrus messages to zerolog for consistent output
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
	zlogger := zerolog.New(consoleWriter).With().Timestamp().Logger()
	logging.InstallZerologHook(zlogger)
}
