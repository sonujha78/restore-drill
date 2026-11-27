package main

import (
	"os"

	"github.com/sonujha78/restore-drill/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
