package main

import (
	"fmt"
	"os"

	"github.com/sonujha78/restore-drill/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "usage: restore-drill version")
	os.Exit(3) // exit 3 = tool error, as in the gate contract
}
