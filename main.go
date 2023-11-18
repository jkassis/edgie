package main

import (
	"net/http"
	"path/filepath"

	"github.com/jkassis/edgie/common"
	"github.com/jkassis/edgie/service"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cmd *cobra.Command

func main() {
	cmd = &cobra.Command{
		Use:   "edgie",
		Short: "edgie serves files from a local directory and falls back to S3 if they don't exist",
		Run:   cmdExecute,
	}

	service.CmdInit(cmd)
	cmd.Execute()
}

func cmdExecute(cmd *cobra.Command, args []string) {
	s, err := service.CmdExecute(cmd, args)
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			path := filepath.Clean(r.URL.Path)
			s.Download(path)
		} else if r.Method == "POST" {
			path := filepath.Clean(r.URL.Path)
			s.Download(path)
			s.Upload(path, r.Body)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
	})

	port := viper.GetString(common.OPT_PORT)
	http.Handle("/metrics", promhttp.Handler())
	log.Warnf("Serving metrics on HTTP port: %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
