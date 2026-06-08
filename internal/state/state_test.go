package state_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/kishibashi3/agent-hub-control/internal/state"
)

// TestLoadLockedConcurrent は並列 LoadLocked → Set → Save が JSON を破損しないことを確認する。
// issue #11: 並列 spawn で bridges.json が破損するバグの再現テスト。
func TestLoadLockedConcurrent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			handle := fmt.Sprintf("bridge-%d", i)

			st, unlock, err := state.LoadLocked()
			if err != nil {
				errs[i] = fmt.Errorf("LoadLocked: %w", err)
				return
			}
			st.Set(handle, 1000+i, "/workdir", "tenant", "/tmp/log")
			if err := st.Save(); err != nil {
				unlock()
				errs[i] = fmt.Errorf("Save: %w", err)
				return
			}
			unlock()
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	// 全エントリが保存されていることを確認
	st, err := state.Load()
	if err != nil {
		t.Fatalf("Load after concurrent writes: %v", err)
	}
	if got := len(st.Bridges); got != n {
		t.Errorf("expected %d bridges, got %d (some entries lost due to race)", n, got)
	}
	for i := 0; i < n; i++ {
		handle := fmt.Sprintf("bridge-%d", i)
		if e := st.Get(handle); e == nil {
			t.Errorf("missing entry for %s", handle)
		}
	}
}
