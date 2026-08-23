package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/igor/openrouter-costwatch/internal/collector"
	"github.com/igor/openrouter-costwatch/internal/openrouter"
	"github.com/igor/openrouter-costwatch/internal/store"
)

//go:embed web/index.html web/app.js web/style.css
var webFS embed.FS

func main() {
	var (
		apiKey   = flag.String("api-key", "", "OpenRouter management key (or env OPENROUTER_MANAGEMENT_KEY)")
		port     = flag.Int("port", 8080, "HTTP listen port")
		interval = flag.Duration("interval", 5*time.Second, "poll interval")
		verbose  = flag.Bool("v", false, "verbose collector logs")
	)
	flag.Parse()

	if *apiKey == "" {
		*apiKey = os.Getenv("OPENROUTER_MANAGEMENT_KEY")
	}
	if *apiKey == "" {
		log.Fatal("OpenRouter management key required (-api-key or OPENROUTER_MANAGEMENT_KEY). Create one at https://openrouter.ai/settings/keys")
	}

	client := openrouter.NewClient(*apiKey)
	if *verbose {
		client.SetLogger(log.Printf)
	}
	st := store.New()
	st.SetInterval(int64(interval.Milliseconds()))
	col := collector.New(client, st, *interval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := col.Discover(ctx); err != nil {
			log.Printf("WARNING: /analytics/meta failed at boot: %v", err)
		}
	}()

	log.Printf("openrouter-spending-cockpit starting (masked key: %s) on :%d interval %s", client.MaskedAPIKey(), *port, *interval)
	go col.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveEmbed(w, "web/index.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		serveEmbed(w, "web/app.js", "application/javascript; charset=utf-8")
	})
	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		serveEmbed(w, "web/style.css", "text/css; charset=utf-8")
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(st.Snapshot()); err != nil {
			log.Printf("state encode: %v", err)
		}
	})
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		streamSSE(w, r, st)
	})

	srv := &http.Server{Addr: ":" + strconv.Itoa(*port), Handler: mux}

	go func() {
		<-ctx.Done()
		shutCtx, cancelShut := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelShut()
		_ = srv.Shutdown(shutCtx)
	}()

	go func() {
		log.Printf("listening on http://127.0.0.1:%d", *port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	cancel()
	log.Println("shutting down")
}

func serveEmbed(w http.ResponseWriter, name, contentType string) {
	data, err := webFS.ReadFile(name)
	if err != nil {
		http.Error(w, "missing "+name, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

// streamSSE pushes a snapshot on each poll tick. The collector shares the
// store; we use a small internal notifier via the store's UpdatedAt by
// polling the store each second and emitting when the version changes.
func streamSSE(w http.ResponseWriter, r *http.Request, st *store.Store) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	last := time.Time{}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	send := func() {
		snap := st.Snapshot()
		fmt.Fprintf(w, "event: tick\ndata: ")
		if err := json.NewEncoder(w).Encode(snap); err != nil {
			_ = err
		}
		fmt.Fprint(w, "\n\n")
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			cur := st.UpdatedAt()
			if cur.After(last) {
				last = cur
				send()
			}
		}
	}
}
