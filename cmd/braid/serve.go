package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/KC1706/braid/internal/run"
)

// runServe starts the Braid engine as an HTTP service: the same RunService the
// CLI uses, exposed so a control plane (or a human with curl) can drive an
// integration. It owns no auth or forking — those are the control plane's job
// (GO_PRODUCTION_PLAN.md Phase 3); this is the in-process transport Phase 1 needs.
func runServe(argv []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	svc := run.Service{Log: func(m string) { log.Println(m) }}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           serveMux(svc),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("braid serve listening on %s\n", *addr)
	fmt.Println("  POST /run      {repoDir, baseline, agents:[{id,branch,intent}], resolver?, sandbox?}")
	fmt.Println("  GET  /healthz")
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
