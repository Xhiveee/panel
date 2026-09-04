package main

import (
	"fmt"
	"os"
)

// printBootstrap prints the one-time initial admin password to stdout.
// It must not go through structured logs (which may be persisted).
func printBootstrap(username, password string) {
	fmt.Fprintf(os.Stdout, "\n=== initial admin account (change the password after login) ===\n  username: %s\n  password: %s\n=============================================================\n\n", username, password)
}
