package main

import (
	"crypto/rand"
	"flag"
	"io"
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

// cryptoRand is the entropy source for ECIES encryption.
func cryptoRand() io.Reader { return rand.Reader }
