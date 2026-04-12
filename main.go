package main

import (
	"os"

	"flow/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:]))
}
