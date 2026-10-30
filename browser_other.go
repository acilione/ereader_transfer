//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

func openBrowser(url string) error {
	program := "xdg-open"
	if runtime.GOOS == "darwin" {
		program = "open"
	}
	return exec.Command(program, url).Run()
}
