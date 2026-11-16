package receiver

import (
	"context"
	"os"
	"pageport/internal/direct"
	"strings"
	"testing"
)

func TestInvalidManifestCannotWriteOutsideBooks(t *testing.T) {
	for _, name := range []string{"../secret.epub", "x\\y.epub", ".hidden", "bad\nname.epub"} {
		j := direct.Job{ID: strings.Repeat("a", 32), Files: []direct.File{{ID: strings.Repeat("b", 32), Name: name, Size: 1, SHA256: strings.Repeat("0", 64)}}}
		c := Client{Output: t.TempDir(), Receipts: t.TempDir()}
		if c.Process(context.Background(), j) == nil {
			t.Fatal("unsafe manifest accepted", name)
		}
		entries, _ := os.ReadDir(c.Output)
		if len(entries) != 0 {
			t.Fatal("unsafe file written")
		}
	}
}
