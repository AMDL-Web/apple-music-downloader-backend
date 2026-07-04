package hooks

import (
	"context"
	"sort"
)

// Runner executes one hook entry against a payload. Adding a new hook type
// only requires implementing Runner and calling register in an init() func;
// Config validation, the Dispatcher, and callers never change.
type Runner interface {
	Run(ctx context.Context, entry Entry, payload Payload) error
}

var runners = map[string]Runner{}

func register(name string, r Runner) {
	runners[name] = r
}

func registeredTypes() []string {
	types := make([]string, 0, len(runners))
	for t := range runners {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}
