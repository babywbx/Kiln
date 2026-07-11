//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/babywbx/kiln/modules/auth"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/hash-password.go <password>")
		os.Exit(2)
	}
	h, err := auth.HashPassword(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(h)
}
