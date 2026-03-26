package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/adyanth/cloudflare-operator/internal/testutil/cfmock"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server, handler := cfmock.NewHandler()

	server.AddAccount("test-account-id", "test-account")
	server.AddZone("test-zone-id", "example.com")

	addr := fmt.Sprintf(":%s", port)
	log.Printf("Mock Cloudflare API server listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
