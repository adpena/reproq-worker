package config

import "flag"

// BindPrivateStringFlag keeps the value out of flag.DefValue and usage/error
// output while retaining normal explicit flag precedence and empty overrides.
func BindPrivateStringFlag(flags *flag.FlagSet, name string, target *string, usage string) {
	flags.Func(name, usage, func(value string) error { *target = value; return nil })
}

func PrivateStringFlag(flags *flag.FlagSet, name, initial, usage string) *string {
	value := initial
	BindPrivateStringFlag(flags, name, &value, usage)
	return &value
}
