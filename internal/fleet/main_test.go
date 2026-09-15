package fleet

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// systemctlCalls records every systemctl invocation the package attempted during tests.
// TestMain swaps execSystemctl for this recorder so no test can reach the host's real
// units — systemctl resolves by unit *name*, and a test once disabled the live user-scope
// agent-hub-fleet.timer this way (issue #55).
var systemctlCalls struct {
	sync.Mutex
	args [][]string
}

func recordSystemctl(args []string, _ bool) ([]byte, error) {
	systemctlCalls.Lock()
	defer systemctlCalls.Unlock()
	systemctlCalls.args = append(systemctlCalls.args, append([]string(nil), args...))
	return nil, nil
}

// resetSystemctlCalls clears the recorder and returns a snapshot function for assertions.
func resetSystemctlCalls(t *testing.T) func() []string {
	t.Helper()
	systemctlCalls.Lock()
	systemctlCalls.args = nil
	systemctlCalls.Unlock()
	return func() []string {
		systemctlCalls.Lock()
		defer systemctlCalls.Unlock()
		out := make([]string, 0, len(systemctlCalls.args))
		for _, a := range systemctlCalls.args {
			out = append(out, strings.Join(a, " "))
		}
		return out
	}
}

func TestMain(m *testing.M) {
	execSystemctl = recordSystemctl
	os.Exit(m.Run())
}
