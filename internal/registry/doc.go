// Package registry turns configured agent names into launch specs. Agents
// named by registry id are looked up in a committed snapshot of the official
// ACP registry, which says whether they are started through npx, uvx or a
// downloaded binary; agents given a command are used as configured.
package registry
