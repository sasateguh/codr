package main

import (
	"os"

	"codr.my/codr/internal/hooks"
)

// cmdHook is what git runs (via the scripts in data/hooks): `amcd hook pre-receive|post-receive`.
// It needs no config: everything comes from the environment serve gave git (D9).
func (c *cli) cmdHook(args []string) int {
	if len(args) != 1 {
		return 2
	}
	return hooks.RunHook(args[0], c.in, c.errOut, os.Getenv)
}
