//go:build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/babywbx/kiln/modules/auth"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	km, err := auth.GenerateEd25519()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	privPath := filepath.Join(dir, "ed25519.pem")
	pubPath := filepath.Join(dir, "ed25519.pub.pem")
	if err := auth.WritePrivateKeyPEM(privPath, km.Private); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := auth.WritePublicKeyPEM(pubPath, km.Public); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", privPath)
	fmt.Printf("wrote %s\n", pubPath)
}
