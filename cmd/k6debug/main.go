package main

import (
	_ "github.com/nhancdt2602/xk6-debug" // registers k6/x/debug module
	"go.k6.io/k6/v2/cmd"
)

func main() {
	cmd.Execute()
}
