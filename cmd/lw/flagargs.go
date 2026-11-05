package main

import (
	"flag"
	"strings"
)

// parseInterspersed parses fs over args allowing flags anywhere; "--" ends flag parsing
// (everything after it is positional). Returns the positionals in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		numRemaining := len(fs.Args())
		numConsumed := len(rest) - numRemaining
		if numConsumed > 0 && hasEndDelimiter(fs, rest[:numConsumed]) {
			positionals = append(positionals, fs.Args()...)
			break
		}
		if numRemaining == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	return positionals, nil
}

// hasEndDelimiter reports whether consumed contains a literal "--" that was
// parsed as the end-of-flags delimiter rather than consumed as a flag value.
func hasEndDelimiter(fs *flag.FlagSet, consumed []string) bool {
	for i := 0; i < len(consumed); {
		token := consumed[i]
		if token == "--" {
			return true
		}
		if !strings.HasPrefix(token, "-") {
			i++
			continue
		}
		if strings.Contains(token, "=") {
			i++
			continue
		}
		name := strings.TrimLeft(token, "-")
		fl := fs.Lookup(name)
		if fl != nil {
			if bf, ok := fl.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
				i++
				continue
			}
		}
		i += 2
	}
	return false
}
