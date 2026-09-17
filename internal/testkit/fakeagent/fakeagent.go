// Package fakeagent is a scripted ACP agent used by every driver test. Its
// replies are decided by the prefix of the incoming prompt. P0-T2 implements
// the behaviour; this file only fixes the contract.
package fakeagent

import (
	"context"
	"io"
)

// Serve runs the fake agent over the given reader and writer until in is
// closed or ctx ends. When in closes, responses for requests already accepted
// are flushed before returning. home is where sessions are persisted.
func Serve(ctx context.Context, in io.Reader, out io.Writer, home string) error {
	panic("not implemented")
}
