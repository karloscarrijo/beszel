// Command jsonserver gives a recorded JSON reply for the local stack of the SNMP bridge.
//
// Env: ADDR (default :8080), REPLY_FILE (path of the JSON), HEADER and HEADER_VALUE
// (the request must carry this header, or the reply is 401), FRESH_PATH (top-level key
// that gets the current time at each reply, default lastHeartbeatAt).
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := env("ADDR", ":8080")
	file := env("REPLY_FILE", "/data/http-device.json")
	header, value := os.Getenv("HEADER"), os.Getenv("HEADER_VALUE")
	freshKey := env("FRESH_PATH", "lastHeartbeatAt")
	raw, err := os.ReadFile(file)
	if err != nil {
		log.Fatalf("read %s: %v", file, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Fatalf("parse %s: %v", file, err)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if header != "" && r.Header.Get(header) != value {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		doc[freshKey] = time.Now().UTC().Format(time.RFC3339)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})
	log.Fatal(http.ListenAndServe(addr, nil))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
