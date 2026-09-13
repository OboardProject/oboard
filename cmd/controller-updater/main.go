package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/logging"
)

const usage = `oboard-controller-updater takes no arguments.

It is a root service started by systemd or OpenRC and reached only through the
Controller-owned Unix socket.
`

func main() {
	log.SetOutput(logging.NewRedactingWriter(os.Stderr))
	// Arguments used to be ignored outright, so any of them - a help flag, a
	// typo - silently started a second privileged updater. A production host
	// was found running two such instances for nine days, each listening on
	// the same control socket as the real service.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			os.Stdout.WriteString(usage)
			return
		}
		os.Stderr.WriteString(usage)
		os.Exit(2)
	}
	if os.Geteuid() != 0 {
		log.Fatal("oboard-controller-updater must run as root")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := controllerupdate.NewService(controllerupdate.DefaultServiceConfig()).Serve(ctx); err != nil {
		log.Fatal(err)
	}
}
