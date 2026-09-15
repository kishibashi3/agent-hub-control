package bridge

import "testing"

// TestParseBridgeCmdlineModel: orphan 取り込みが spawn の "-model <id>" を Entry.Model に復元できる (issue #46)。
// spawn は "-model" で渡すが、手動起動の "--model" も Go flag では同義なので両方拾う。
func TestParseBridgeCmdlineModel(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"spawn form -model", []string{"/x/bridge-claude2", "--participant", "a", "--workdir", "/w", "--tenant", "t", "-model", "m1"}, "m1"},
		{"manual form --model", []string{"bridge-claude2", "--participant", "a", "--model", "m2"}, "m2"},
		{"no model", []string{"bridge-claude2", "--participant", "a", "--workdir", "/w"}, ""},
		{"trailing -model without value is ignored", []string{"bridge-claude2", "--participant", "a", "-model"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handle, workdir, tenant, bridgeType, model := parseBridgeCmdline(tc.args)
			if handle != "a" {
				t.Errorf("handle = %q, want a", handle)
			}
			if model != tc.want {
				t.Errorf("model = %q, want %q (workdir=%q tenant=%q type=%q)", model, tc.want, workdir, tenant, bridgeType)
			}
		})
	}
}
