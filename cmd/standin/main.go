// Command standin serves the fake model provider on a local port.
//
//	go run ./cmd/standin -addr :8090 -config scenario.json
//
// Point llm.NewHTTP at http://localhost:8090 to use it.
package main

import (
	"flag"
	"log"
	"net/http"

	"relay/internal/standin"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	path := flag.String("config", "", "JSON overrides on top of the probe-calibrated defaults")
	flag.Parse()

	cfg := standin.Default()
	if *path != "" {
		var err error
		if cfg, err = standin.Load(*path); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("stand-in on %s (calibrated from %s; RPM=%d TPM=%d RPD=%d TPD=%d in-flight cap=%d, 5xx=%.3f drop=%.3f)",
		*addr, cfg.Source, cfg.RPM, cfg.TPM, cfg.RPD, cfg.TPD, cfg.MaxInFlight, cfg.ServerErrorRate, cfg.DropRate)
	log.Fatal(http.ListenAndServe(*addr, standin.New(cfg)))
}
