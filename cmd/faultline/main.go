// Command faultline generates scenarios, runs diagnosers and scores them.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = cmdGen(os.Args[2:])
	case "agent":
		err = cmdAgent(os.Args[2:])
	case "rules":
		err = cmdRules(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "faultline:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  faultline gen   -split dev|test -out DIR
  faultline rules -data DIR [-full] -out FILE.json
  faultline agent -data DIR [-model M] [-think] -out FILE.json -traces DIR`)
	os.Exit(2)
}
