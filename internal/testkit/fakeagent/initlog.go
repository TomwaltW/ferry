package fakeagent

import (
	"fmt"
	"os"
	"path/filepath"
)

// InitializeLog is the file under home where both modes record the protocol
// version of every initialize they answered, one per line. It is how a driver
// test sees a negotiation that happened inside a subprocess: an agent that was
// probed over v2 and then started again over v1 leaves two lines behind.
const InitializeLog = "initializes.log"

// recordInitialize appends one initialize to the log, and says nothing when it
// cannot: the log is a test aid, never a reason for the agent to fail.
func recordInitialize(st *store, version int) {
	f, err := os.OpenFile(filepath.Join(st.home(), InitializeLog), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%d\n", version)
}
