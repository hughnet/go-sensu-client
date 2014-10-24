package main

import (
	"checks"
	"flag"
	"log"
	"os"
	"sensu"
	"strings"
)

var configFile, configDir string

func init() {
	flag.StringVar(&configFile, "config-file", "config.json", "Sensu JSON config file")
	flag.StringVar(&configDir, "config-dir", "conf.d", "directory or comma-delimited directory list for Sensu JSON config files")
	flag.Parse()
}

func main() {
	configDirs := strings.Split(configDir, ",")
	settings, err := sensu.LoadConfigs(configFile, configDirs)
	if err != nil {
		log.Printf("Unable to load settings: %s", err)
		flag.Usage()
		os.Exit(1)
	}

	processes := []sensu.Processor{
		new(sensu.Keepalive),
		new(sensu.Subscriber),
		checks.NewProcessor(),
	}
	c := sensu.NewClient(settings, processes)

	c.Start()
}