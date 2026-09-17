// Command check-i18n verifies that the English and Chinese documentation and
// the locale catalogs stay in step. The rules are frozen in spec-p0 3.7.
//
// Usage: check-i18n [--root <dir>] [--strict]
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	root := flag.String("root", ".", "repository root to check")
	strict := flag.Bool("strict", false, "treat a stale source_commit as an error instead of a warning")
	flag.Parse()

	r := check(*root, *strict)
	for _, w := range r.warnings {
		fmt.Fprintln(os.Stderr, w)
	}
	if len(r.problems) == 0 {
		fmt.Println("i18n ok")
		return
	}
	for _, p := range r.problems {
		fmt.Fprintln(os.Stderr, p)
	}
	os.Exit(1)
}
