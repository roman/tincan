package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptionRequiresHostAdmission(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			root, args := adoptionCLIFixture(t)
			if mode == "unauthorized" {
				serveTaskRoot(t, root)
			}
			path := filepath.Join(root, "records", "task-1.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			code := Run(
				context.Background(),
				append([]string{"--root", root}, args...),
				nil,
				&output,
				io.Discard,
			)
			if code != 1 {
				t.Fatalf("unadmitted adoption code %d: %s", code, &output)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("unadmitted adoption changed record: %v", err)
			}
		})
	}
}
