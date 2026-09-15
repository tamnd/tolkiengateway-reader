package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags, and falls back to "dev" for a
// plain go build or go run.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println("tgw " + version)
	case "publish":
		err = runPublish(os.Args[2:])
	case "audit":
		err = runAudit(os.Args[2:])
	case "acquire":
		err = runAcquire(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, os.Args[1]+": "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tgw <version|acquire|publish|audit> [flags]")
}
