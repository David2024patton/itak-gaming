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
	"path/filepath"
	"syscall"
	"time"

	"github.com/davidpatton/itak-gaming/netplay/internal/appdist"
	"github.com/davidpatton/itak-gaming/netplay/internal/debugchute"
	"github.com/davidpatton/itak-gaming/netplay/internal/friends"
	"github.com/davidpatton/itak-gaming/netplay/internal/lobby"
	"github.com/davidpatton/itak-gaming/netplay/internal/matches"
	"github.com/davidpatton/itak-gaming/netplay/internal/server"
	"github.com/davidpatton/itak-gaming/netplay/internal/stats"
)

// defaultAppDir is where published app builds live for self-update:
// the ITAK_APP_DIR environment variable wins; empty disables the
// /app routes entirely.
func defaultAppDir() string {
	return os.Getenv("ITAK_APP_DIR")
}

// defaultDebugDir is where debug chute reports land: the
// ITAK_DEBUG_DIR environment variable wins; empty disables the
// /debug routes entirely.
func defaultDebugDir() string {
	return os.Getenv("ITAK_DEBUG_DIR")
}

// defaultStatsFile is where the global leaderboard persists: the
// ITAK_STATS_FILE environment variable wins, else a file next to
// wherever the server runs.
func defaultStatsFile() string {
	if p := os.Getenv("ITAK_STATS_FILE"); p != "" {
		return p
	}
	return "./stats.json"
}

// defaultFriendsFile is where friends lists persist: the
// ITAK_FRIENDS_FILE environment variable wins, else a friends.json
// next to the stats file, so both state files live in the same
// directory (in production, the same mounted volume).
func defaultFriendsFile(statsFile string) string {
	if p := os.Getenv("ITAK_FRIENDS_FILE"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(statsFile), "friends.json")
}

// defaultMatchesFile is where tournament results persist: the
// ITAK_MATCHES_FILE environment variable wins, else a matches.json
// next to the stats file, alongside the friends file.
func defaultMatchesFile(statsFile string) string {
	if p := os.Getenv("ITAK_MATCHES_FILE"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(statsFile), "matches.json")
}

func main() {
	addr := flag.String("addr", ":8790", "listen address (host:port)")
	sweepInterval := flag.Duration("sweep", 15*time.Second, "lobby reaper interval")
	statsFile := flag.String("statsfile", defaultStatsFile(), "path to the global leaderboard stats file")
	friendsFile := flag.String("friendsfile", "", "path to the friends list file (default: friends.json next to the stats file)")
	matchesFile := flag.String("matchesfile", "", "path to the tournament results file (default: matches.json next to the stats file)")
	appDir := flag.String("appdir", defaultAppDir(), "directory with published app builds (version.json + APK) for self-update; empty disables")
	debugDir := flag.String("debugdir", defaultDebugDir(), "directory for debug chute reports from app installs; empty disables")
	flag.Parse()

	friendsPath := *friendsFile
	if friendsPath == "" {
		friendsPath = defaultFriendsFile(*statsFile)
	}
	matchesPath := *matchesFile
	if matchesPath == "" {
		matchesPath = defaultMatchesFile(*statsFile)
	}

	logger := log.New(os.Stdout, "lobbyserver ", log.LstdFlags)
	store := lobby.NewMemoryStore()
	srv := server.New(store, logger)

	// Global leaderboard: same binary, same port. Apps report their
	// players' cumulative stats; the rankings are served from memory
	// and persisted to the stats file in the background.
	statsStore := stats.Open(*statsFile, logger)
	statsHandler := stats.NewHandler(statsStore, logger)

	// Friends lists: same binary, same port. Players follow other
	// players by name; reads enrich each friend with the identity
	// the leaderboard knows and with live lobby presence.
	friendsStore := friends.Open(friendsPath, logger)
	friendsHandler := friends.NewHandler(friendsStore, statsStore, store, logger)

	// Tournament results: same binary, same port. Host clients
	// report match outcomes and champions; the board is served
	// from memory and persisted to the matches file in the
	// background, separate from the playtime leaderboard.
	matchesStore := matches.Open(matchesPath, logger)
	matchesHandler := matches.NewHandler(matchesStore, statsStore, logger)

	// Reaper: expired lobbies are collected in the background so the
	// request path never pays for cleanup.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reapLoop(ctx, store, *sweepInterval, logger)
	go statsStore.SaveLoop(ctx, 10*time.Second)
	go friendsStore.SaveLoop(ctx, 10*time.Second)
	go matchesStore.SaveLoop(ctx, 10*time.Second)

	mux := http.NewServeMux()
	statsHandler.Register(mux)
	friendsHandler.Register(mux)
	matchesHandler.Register(mux)
	if *appDir != "" {
		appdist.New(*appDir, logger).Register(mux)
		logger.Printf("app updates served from %s", *appDir)
	}
	if *debugDir != "" {
		debugchute.New(*debugDir, logger).Register(mux)
		logger.Printf("debug chute reports stored under %s", *debugDir)
	}
	mux.Handle("/", srv.Handler())

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           mux,
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
