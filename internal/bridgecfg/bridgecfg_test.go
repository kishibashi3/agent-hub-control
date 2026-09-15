package bridgecfg

import "testing"

// TestSaveLoadRoundTripModel: model は保存・復元され、未設定なら JSON に出ない (omitempty、旧ファイル互換)。
func TestSaveLoadRoundTripModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := Save(&BridgeConfig{Handle: "alpha", Workdir: "/w", Model: "some-model-id"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load("alpha")
	if err != nil || got == nil {
		t.Fatalf("load: %v (%+v)", err, got)
	}
	if got.Model != "some-model-id" || got.Workdir != "/w" {
		t.Errorf("round-trip = %+v, want Model=some-model-id Workdir=/w", got)
	}

	if err := Save(&BridgeConfig{Handle: "beta", Workdir: "/w"}); err != nil {
		t.Fatalf("save beta: %v", err)
	}
	if got, err := Load("beta"); err != nil || got == nil || got.Model != "" {
		t.Errorf("unset model should load as empty, got %+v (%v)", got, err)
	}
}
