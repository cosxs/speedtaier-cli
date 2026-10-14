package main

import (
	"os"

	"github.com/cosxs/speedtaier-cli/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
