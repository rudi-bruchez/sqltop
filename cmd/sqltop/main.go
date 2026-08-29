// Command sqltop is a top for SQL servers.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/rudi-bruchez/sqltop/internal/buildinfo"
	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/dotenv"
)

func main() {
	configPath := flag.String("config", "", "path to sqltop.json (default: beside the binary, then the user config directory)")
	envPath := flag.String("env", ".env", "path to the .env file holding secrets")
	showConfig := flag.Bool("show-config", false, "print the resolved configuration and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.String())
		return
	}

	if err := dotenv.Load(*envPath); err != nil {
		log.Printf("warning: %s: %v", *envPath, err)
	}

	path, err := config.Resolve(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatal(err)
	}

	if *showConfig {
		where := cfg.Path
		if where == "" {
			where = "(built-in defaults, no file found)"
		}
		fmt.Fprintln(os.Stderr, "configuration from:", where)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	log.Fatal("not implemented yet: run with -show-config")
}
