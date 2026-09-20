// backup-verify validates a synthetic or operator-supplied archive without activating it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/backup"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, passwordInput *os.File, output, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("backup-verify", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	archive := flags.String("archive", "", "explicit local backup archive")
	parent := flags.String("temp-parent", "", "existing private absolute temporary directory; crash leftovers must be removed by the operator")
	version := flags.String("target-version", "", "target Controller version")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *archive == "" || *parent == "" || *version == "" {
		fmt.Fprintln(diagnostic, "archive, temp-parent and target-version are required; supply password via redirected stdin")
		return 2
	}
	info, err := passwordInput.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintln(diagnostic, "password must arrive via a pipe or private file on stdin, not an interactive terminal")
		return 2
	}
	if info.Mode().IsRegular() && info.Mode().Perm()&0077 != 0 {
		fmt.Fprintln(diagnostic, "password file must not be accessible by group or others")
		return 2
	}
	password, err := io.ReadAll(io.LimitReader(passwordInput, 4097))
	if err != nil || len(password) > 4096 {
		fmt.Fprintln(diagnostic, "cannot read bounded password input")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	report, err := backup.VerifyRestore(ctx, backup.VerifyRestoreOptions{ArchivePath: *archive, TempParent: *parent, TargetVersion: *version, Password: strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r")})
	if encodeErr := json.NewEncoder(output).Encode(report); encodeErr != nil {
		return 1
	}
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	return 0
}
