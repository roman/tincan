package conciergecli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHerdrExpectationResolvesNameAndPathIdentically(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"herdr", "other-herdr"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory)
	named, err := resolveHerdrExecutable("herdr")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := resolveHerdrExecutable(filepath.Join(directory, "herdr"))
	if err != nil {
		t.Fatal(err)
	}
	different, err := resolveHerdrExecutable("other-herdr")
	if err != nil {
		t.Fatal(err)
	}
	if named != absolute || named == different {
		t.Fatalf("name %s absolute %s different %s", named, absolute, different)
	}
}
