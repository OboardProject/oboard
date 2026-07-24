package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/OboardProject/oboard/internal/controllerupdate"
)

func main() {
	if os.Geteuid() != 0 {
		log.Fatal("oboard-controller-updater must run as root")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := controllerupdate.NewService(controllerupdate.DefaultServiceConfig()).Serve(ctx); err != nil {
		log.Fatal(err)
	}
}
