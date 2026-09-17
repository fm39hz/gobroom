package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorouter/gorouter/internal/api"
	"github.com/gorouter/gorouter/internal/store"
)

func main() {
	defaultDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	defaultDB := filepath.Join(defaultDir, "gorouter", "gorouter.db")

	dbPath := flag.String("db", defaultDB, "SQLite database path")
	addr := flag.String("addr", "127.0.0.1:20127", "control/data plane listen address")
	flag.Parse()

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	server := api.NewServer(db)
	log.Printf("gorouterd listening on %s", *addr)
	if err := http.ListenAndServe(*addr, server.Handler()); err != nil {
		log.Fatal(err)
	}
}
