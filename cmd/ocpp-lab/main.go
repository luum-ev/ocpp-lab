// ocpp-lab: an OCPP 1.6J charge point fleet emulator.
//
//	ocpp-lab serve --fleet fleet.yaml --listen :8887
//	ocpp-lab status
//	ocpp-lab plug STATION/1 · unplug · charge · stop · kill · offline · online
//
// The CLI subcommands are thin clients of the REST API — everything they can
// do, CI can do with curl.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/luum-ev/ocpp-lab/internal/api"
	"github.com/luum-ev/ocpp-lab/internal/fleet"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "status":
		client("GET", "/stations", os.Args[2:], "")
	case "plug", "unplug", "stop":
		target := requireTarget(os.Args)
		client("POST", "/stations/"+target[0]+"/connectors/"+target[1]+"/"+os.Args[1], os.Args[3:], "")
	case "charge":
		target := requireTarget(os.Args)
		client("POST", "/stations/"+target[0]+"/connectors/"+target[1]+"/charge", os.Args[3:], "{}")
	case "fault":
		target := requireTarget(os.Args)
		client("POST", "/stations/"+target[0]+"/connectors/"+target[1]+"/fault", os.Args[3:], "{}")
	case "kill", "offline", "online":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "usage: ocpp-lab %s STATION\n", os.Args[1])
			os.Exit(2)
		}
		client("POST", "/stations/"+os.Args[2]+"/"+os.Args[1], os.Args[3:], "")
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `ocpp-lab — OCPP 1.6J fleet emulator

  serve   --fleet fleet.yaml [--listen :8887]   run the fleet + control API
  status                                        list stations and connectors
  plug    STATION/CONNECTOR                     connect the cable
  unplug  STATION/CONNECTOR                     remove the cable
  charge  STATION/CONNECTOR                     start a local transaction
  stop    STATION/CONNECTOR                     stop the transaction
  fault   STATION/CONNECTOR                     force a Faulted status
  kill    STATION                               drop TCP without a Close frame
  offline STATION                               go offline (sessions keep running, messages queue)
  online  STATION                               reconnect and flush the queue

Client commands accept --api http://localhost:8887 (default).`)
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fleetPath := fs.String("fleet", "fleet.yaml", "fleet file (declarative station list)")
	listen := fs.String("listen", ":8887", "control API address")
	_ = fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	fl, err := fleet.Load(*fleetPath, log)
	if err != nil {
		log.Error("cannot load fleet", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: *listen, Handler: (&api.Server{Fleet: fl, Log: log}).Handler()}
	go func() {
		log.Info("control API listening", "addr", *listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("control API failed", "error", err)
			os.Exit(1)
		}
	}()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	log.Info("fleet starting", "csms", fl.CSMS)
	fl.Run(ctx)
}

func requireTarget(args []string) [2]string {
	if len(args) < 3 || !strings.Contains(args[2], "/") {
		fmt.Fprintf(os.Stderr, "usage: ocpp-lab %s STATION/CONNECTOR\n", args[1])
		os.Exit(2)
	}
	parts := strings.SplitN(args[2], "/", 2)
	return [2]string{parts[0], parts[1]}
}

func client(method, path string, args []string, body string) {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	apiURL := fs.String("api", "http://localhost:8887", "control API base URL")
	_ = fs.Parse(args)

	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, *apiURL+path, reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	fmt.Println(strings.TrimSpace(string(out)))
	if resp.StatusCode >= 400 {
		os.Exit(1)
	}
}
