package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"os"
)

// flagSet is a thin wrapper so each subcommand declares its flags inline.
type flagSet struct {
	fs   *flag.FlagSet
	args []string
}

func flags(name string, args []string, _ map[string]*string, _ any) *flagSet {
	return &flagSet{fs: flag.NewFlagSet(name, flag.ExitOnError), args: args}
}

func (f *flagSet) str(name, def, usage string) *string         { return f.fs.String(name, def, usage) }
func (f *flagSet) i64(name string, def int64, u string) *int64 { return f.fs.Int64(name, def, u) }
func (f *flagSet) u64(name string, def uint64, u string) *uint64 {
	return f.fs.Uint64(name, def, u)
}
func (f *flagSet) parse() error { return f.fs.Parse(f.args) }

// secret declares a flag whose value is a credential, and prefers the named
// environment variable over it.
//
// A value passed as a flag is in argv, which every other process on the machine
// can read from `ps`, and in the shell history of whoever ran it. The flag stays
// for local testing and stays documented, but anything handling a real
// credential should set the variable instead — see scripts/upload-hex-key.sh.
//
// Returns a getter rather than a pointer because the environment is only
// consulted after parse().
func (f *flagSet) secret(name, env, usage string) func() string {
	flagValue := f.fs.String(name, "", usage+" (prefer the "+env+" environment variable)")
	return func() string {
		if v := os.Getenv(env); v != "" {
			if *flagValue != "" {
				fmt.Fprintf(os.Stderr,
					"warning: both -%s and %s are set; using %s\n", name, env, env)
			}
			return v
		}
		if *flagValue != "" {
			fmt.Fprintf(os.Stderr,
				"warning: -%s puts a credential in argv, where any local process can read it; "+
					"set %s instead\n", name, env)
		}
		return *flagValue
	}
}

// cryptoRand is the entropy source for ECIES encryption.
func cryptoRand() io.Reader { return rand.Reader }
