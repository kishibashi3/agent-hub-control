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

// recordSystemctl records argv for a plain systemctl call, and name+argv when the package
// wrapped the call (e.g. `sudo -u <user> env … systemctl --user …`, issue #44) so assertions on
// the plain form stay unchanged while the wrapper is still visible.
func recordSystemctl(name string, argv []string, _ bool) ([]byte, error) {
	systemctlCalls.Lock()
	defer systemctlCalls.Unlock()
	rec := append([]string(nil), argv...)
	if name != "systemctl" {
		rec = append([]string{name}, rec...)
	}
	systemctlCalls.args = append(systemctlCalls.args, rec)
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
