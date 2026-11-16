//go:build linux

package main

import (
	"context"
	"os"
	"pageport/internal/direct"
	"path/filepath"
	"testing"
)

func TestQndbNativeSignalOutput(t *testing.T) {
	for _, test := range []struct {
		output string
		accept bool
		fails  bool
	}{{"dlgConfirmResult 1", true, false}, {"dlgConfirmResult 0", false, false}, {"1", false, true}, {"unexpected", false, true}} {
		t.Run(test.output, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "qndb")
			if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+test.output+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			accepted, err := (nickel{path}).Confirm(context.Background(), direct.Job{Files: []direct.File{{Name: "Book.epub"}}})
			if accepted != test.accept || (err != nil) != test.fails {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
		})
	}
}
