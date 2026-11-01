package main

import "flag"

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
		if numConsumed > 0 && rest[numConsumed-1] == "--" {
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
