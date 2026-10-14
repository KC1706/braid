package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/KC1706/braid/internal/run"
	"github.com/KC1706/braid/internal/serve"
)

// runServe starts the Braid engine as an HTTP service. Two shapes:
//
//   - `-zone io|compute` mounts the two-zone container API (Phase 3c): one zone
//     per Cloudflare Container. The io zone clones/pushes with the Artifacts
//     tokens; the compute zone merges + runs the agents' tests with no token and
//     (via the container's network posture) no egress. Zone also reads BRAID_ZONE.
//   - no zone mounts the legacy single-process transport (`POST /run` taking a
//     prepared repoDir) the CLI/Phase-1 path uses.
//
// It owns no auth or forking — the control-plane Worker does (Phase 3b).
func runServe(argv []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	zone := fs.String("zone", "", "container zone: io|compute (empty = legacy single-process /run)")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *zone == "" {
		*zone = os.Getenv("BRAID_ZONE")
	}

	var handler http.Handler
	switch *zone {
	case serve.ZoneIO, serve.ZoneCompute:
		handler = serve.Handler(*zone)
		fmt.Printf("braid serve [zone=%s] listening on %s\n", *zone, *addr)
	case "":
		handler = serveMux(run.Service{Log: func(m string) { log.Println(m) }})
		fmt.Printf("braid serve listening on %s\n", *addr)
		fmt.Println("  POST /run      {repoDir, baseline, agents:[{id,branch,intent}], resolver?, sandbox?}")
		fmt.Println("  GET  /healthz")
	default:
		return fmt.Errorf("unknown -zone %q (want io|compute)", *zone)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
	}
	return srv.ListenAndServe()
}

// serveMux builds the HTTP routes for the engine service.
func serveMux(svc run.Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		var spec run.RunSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request: " + err.Error()})
			return
		}
		res, err := svc.Run(r.Context(), spec)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
