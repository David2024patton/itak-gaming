package main

import (
	"flag"
	"fmt"
	"log"
	"runtime"

	"github.com/davidpatton/itak-gaming/go_backend/internal/server"
)

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:8081", "API server address")
		saveDir = flag.String("savedir", "./data", "Save data directory")
		romDir  = flag.String("romdir", "./roms", "ROM directory")
	)
	flag.Parse()

	is64Bit := runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	fmt.Printf("iTaK Gaming Backend (64-bit: %v)\n", is64Bit)

	srv := server.New(*saveDir, *romDir, is64Bit)
	log.Printf("Starting API server on %s", *addr)
	if err := srv.Start(*addr); err != nil {
		log.Fatal(err)
	}
}
