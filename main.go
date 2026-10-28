package main

import (
	"github.com/InazumaV/V2bX/cmd"
	"os"
)

func main() {
	if cmd.Run() != nil {
		os.Exit(1)
	}
}
