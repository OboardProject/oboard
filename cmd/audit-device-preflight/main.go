package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/OboardProject/oboard/internal/store"
)

func run(args []string, out io.Writer) int {
	flags := flag.NewFlagSet("audit-device-preflight", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("database", "", "existing SQLite database")
	if flags.Parse(args) != nil || *path == "" || flags.NArg() != 0 {
		fmt.Fprintln(out, "usage: audit-device-preflight -database PATH")
		return 2
	}
	result, err := store.PreviewDeviceRetirementDatabase(context.Background(), *path)
	if err != nil {
		fmt.Fprintln(out, "device retirement preflight failed; database unavailable or schema unsupported")
		return 1
	}
	if json.NewEncoder(out).Encode(result) != nil {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }
