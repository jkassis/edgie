package common

import (
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	OPT_PORT      = "PORT"
	OPT_LOG_LEVEL = "LOG_LEVEL"
)

func CmdInit(Cmd *cobra.Command) {
	Cmd.PersistentFlags().String(OPT_PORT, "8080", "Port to serve API and metrics")
	viper.BindPFlag(OPT_PORT, Cmd.PersistentFlags().Lookup(OPT_PORT))

	Cmd.PersistentFlags().String(OPT_LOG_LEVEL, "WARN", "Log level for the whole proc")
	viper.BindPFlag(OPT_LOG_LEVEL, Cmd.PersistentFlags().Lookup(OPT_LOG_LEVEL))
}

func CmdExecute(cmd *cobra.Command, args []string) {
	viper.AutomaticEnv()
}

func CmdRun() {
	// Log as JSON instead of the default ASCII formatter.
	log.SetFormatter(&log.JSONFormatter{})

	// Output to stdout instead of the default stderr
	// Can be any io.Writer, see below for File example
	log.SetOutput(os.Stdout)

	// Only log the warning severity or above.
	logLevel := viper.GetString(OPT_LOG_LEVEL)
	level, err := log.ParseLevel(logLevel)
	if err != nil {
		log.Panic("LOG_LEVEL Invalid")
		panic(err)
	}

	log.SetLevel(level)
}
