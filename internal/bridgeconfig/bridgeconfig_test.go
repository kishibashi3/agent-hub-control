package bridgeconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Bridges) != 0 {
		t.Errorf("expected empty bridges, got %d", len(cfg.Bridges))
	}
}

func TestSetGetRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	cfg.Set("planner", &BridgeEntry{
		Workdir:     "/work/planner",
		Tenant:      "kaz",
		BridgeType:  "bridge-claude2",
		DisplayName: "Planner — design",
	})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	// reload from disk
	cfg2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	e := cfg2.Get("planner")
	if e == nil {
		t.Fatal("expected entry for planner, got nil")
	}
	if e.Workdir != "/work/planner" {
		t.Errorf("workdir: got %q, want %q", e.Workdir, "/work/planner")
	}
	if e.Tenant != "kaz" {
		t.Errorf("tenant: got %q, want %q", e.Tenant, "kaz")
	}
	if e.BridgeType != "bridge-claude2" {
		t.Errorf("bridge_type: got %q, want %q", e.BridgeType, "bridge-claude2")
	}
	if e.DisplayName != "Planner — design" {
		t.Errorf("display_name: got %q, want %q", e.DisplayName, "Planner — design")
	}
}

func TestSavePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, _ := Load()
	cfg.Set("test", &BridgeEntry{Workdir: "/tmp/test"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(dir, "agenthubctl", "config.json")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected file at %s: %v", expected, err)
	}
}

func TestGetMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, _ := Load()
	if e := cfg.Get("nonexistent"); e != nil {
		t.Errorf("expected nil, got %+v", e)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "agenthubctl")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{invalid`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
