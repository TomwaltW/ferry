// Package conformance checks Ferry's mcpmini against the official MCP Go SDK.
//
// Every test here runs twice: once with mcpmini on both ends, and once with
// the official SDK as the peer. A difference between the two runs is a place
// where Ferry would misread a real MCP implementation — or be misread by one —
// which is what `ferry channel` and the third-party channel servers depend on.
package conformance
