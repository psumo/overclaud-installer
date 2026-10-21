// fakeapi stands in for overclaud.dev in end-to-end tests: one valid key, and the release
// tarball named on the command line served through the same two-step download.
//
//	fakeapi -addr :8765 -key GC-TEST-0000-0000 -sha abc123 -tarball release.tar.gz
//
// The served tarball can be swapped between runs by writing a new file to the same path
// and changing -sha via the /admin/sha?v= endpoint.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"sync"
)

func main() {
	addr := flag.String("addr", ":8765", "listen address")
	key := flag.String("key", "GC-TEST-0000-0000", "the one valid licence key")
	sha := flag.String("sha", "test0001", "release id reported to the installer")
	tarball := flag.String("tarball", "release.tar.gz", "release tarball to serve")
	flag.Parse()
	var mu sync.Mutex
	current := *sha

	http.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if body["key"] != *key || body["deviceId"] == "" {
			json.NewEncoder(w).Encode(map[string]any{"valid": false, "message": "License not active."})
			return
		}
		mu.Lock()
		s := current
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"valid": true, "url": "http://" + r.Host + "/api/fetch?t=token", "sha": s, "email": "test@example.com", "plan": "lifetime"})
	})
	http.HandleFunc("/api/fetch", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "token" {
			http.Error(w, "expired", http.StatusForbidden)
			return
		}
		data, err := os.ReadFile(*tarball)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(data)
	})
	http.HandleFunc("/admin/sha", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		current = r.URL.Query().Get("v")
		mu.Unlock()
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}
