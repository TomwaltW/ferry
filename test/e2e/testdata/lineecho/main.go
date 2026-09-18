// Command lineecho reads its input a line at a time and answers every line
// with "got:<line>". It is the Windows stand-in for the shell loop B9 wraps on
// Unix (`while read l; do echo got:$l; done`), which Windows has no shell for.
//
// A line ends at a carriage return or a line feed, so a line typed into a
// terminal and a line written down a pipe both count. The line is echoed as
// it arrived, escape sequences and all; the same is true of the shell loop,
// and cleaning up what goes back to chat is ferry run's job, not this one's.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
)

func main() {
	restore := consoleInput()
	defer restore()

	in := bufio.NewReader(os.Stdin)
	var line []byte
	for {
		b, err := in.ReadByte()
		if err != nil {
			if err != io.EOF {
				fmt.Fprintln(os.Stderr, "lineecho:", err)
			}
			return
		}
		switch b {
		case '\r', '\n':
			// A CR LF pair would otherwise read as a line and an empty one.
			if len(line) == 0 && b == '\n' {
				continue
			}
			fmt.Printf("got:%s\n", line)
			line = line[:0]
		default:
			line = append(line, b)
		}
	}
}
