package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
)

func main() {
	addr := flag.String("addr", "http://127.0.0.1:20127", "gorouterd address")
	flag.Parse()

	resp, err := http.Get(*addr + "/api/status")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gorouterd is unreachable:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var value any
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}
