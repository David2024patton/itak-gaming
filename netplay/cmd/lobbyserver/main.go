// Command lobbyserver runs the iTaK Gaming netplay lobby service.
//
// It is deliberately small: lobby create/list/join over HTTP, and a
// WebSocket signaling relay that passes messages between the two peers
// of a lobby verbatim. Game traffic itself flows peer-to-peer when the
// clients can manage it; this server only introduces the players and
// carries whatever they ask it to relay. See PROTOCOL.md for the full
// netplay design.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/davidpatton/itak-gaming/netplay/internal/lobby"
	"github.com/davidpatton/itak-gaming/netplay/internal/server"
)

func main() {
	addr := flag.String("addr", ":8790", "listen address (host:port)")
	sweepInterval := flag.Duration("sweep", 15*time.Second, "lobby reaper interval")
	flag.Parse()

	logger := log.New(os.Stdout, "lobbyserver ", log.LstdFlags)
	store := lobby.NewMemoryStore()
	srv := server.New(store, logger)

	// Reaper: expired lobbies are collected in the background so the
	// request path never pays for cleanup.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reapLoop(ctx, store, *sweepInterval, logger)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	logger.Printf("listening on %s", *addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("serve: %v", err)
	}
	logger.Print("shut down")
}

func reapLoop(ctx context.Context, store lobby.Store, interval time.Duration, logger *log.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if n := store.Sweep(now); n > 0 {
				logger.Printf("reaper: removed %d expired lobbies (%d remain)", n, store.Count())
			}
		}
	}
}
