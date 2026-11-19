// Command memory is a local memory for coding agents.
package main

import (
	"os"

	"memory/internal/cli"
)

func main() {
	cwd, _ := os.Getwd()
	os.Exit(cli.Main(os.Args[1:], cli.Env{
		Getenv: os.Getenv, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Cwd: cwd,
	}))
}
