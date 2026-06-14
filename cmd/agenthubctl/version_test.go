package main

import (
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	got := versionString()

	for _, want := range []string{"agenthubctl ", "commit:", "built:", "go:"} {
		if !strings.Contains(got, want) {
			t.Errorf("versionString() missing %q\n got: %q", want, got)
		}
	}

	// 4 行（version / commit / built / go）出力されること。
	lines := strings.Count(strings.TrimRight(got, "\n"), "\n") + 1
	if lines != 4 {
		t.Errorf("versionString() expected 4 lines, got %d:\n%s", lines, got)
	}
}
