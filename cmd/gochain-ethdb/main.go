package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err == flag.ErrHelp {
		os.Exit(1)
	} else if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var cmd string
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "", "help":
		PrintUsage()
		return nil
	case "check":
		return NewCheckCommand().Run(args)
	default:
		return fmt.Errorf("unknown command: %q", cmd)
	}
}

func PrintUsage() {
	fmt.Fprintln(os.Stderr, `
This is a tool for inspecting and working with ethdb database files.

Usage:

	gochain-ethdb command [arguments]

The commands are:

	check       verify integrity of a segment
	help        print this screen
`[1:])
}
