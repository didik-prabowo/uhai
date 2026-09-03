package main

import (
	"fmt"
	"os"

	"github.com/didik-prabowo/ouhai/internal/config"
)

func main() {
	os.Chdir(os.Args[1])
	for _, c := range [][2]string{
		{"run_bash", "git status --short"},
		{"run_bash", "git status && ls -la"},
		{"run_bash", "git status && rm -rf /"},
		{"run_bash", "git push origin main"},
		{"run_bash", "git log `whoami`"},
		{"run_bash", "npm install"},
		{"write_file", "catatan.txt"},
		{"read_file", "internal/main.go"},
		{"read_file", ".env"},
	} {
		fmt.Printf("  %-10s %-28s → %s\n", c[0], c[1], config.Permission(c[0], c[1]))
	}
}
