package main

import (
	"flag"
	"fmt"

	firevm "github.com/edgeforge/firecracker-task-driver/driver"
	log "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/plugins"
)

func main() {
	version := flag.Bool("version", false, "show driver version")
	flag.Parse()
	if *version {
		fmt.Println(firevm.PluginVersion)
		return
	}
	// Serve the plugin
	plugins.Serve(factory)
}

func factory(log log.Logger) interface{} {
	return firevm.NewFirecrackerDriver(log)
}
