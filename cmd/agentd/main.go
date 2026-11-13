// Command agentd runs the personal agent service.
//
// Wiring order matters: store -> gateway -> executor -> scheduler. The gateway is
// constructed before any executor so that no executor can exist without a guard.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"personal-agent/internal/api"
	"personal-agent/internal/domain"
	"personal-agent/internal/executor"
	"personal-agent/internal/gateway"
	"personal-agent/internal/observability"
	"personal-agent/internal/planner"
	"personal-agent/internal/scheduler"
	"personal-agent/internal/store"
	"personal-agent/internal/verifier"
)

func main() {
	addr := env("ADDR", ":8080")
	dataPath := env("DATA_PATH", "data/state.json")
	tick := durationEnv("TICK", 15*time.Second)

	st, err := store.Open(dataPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	if _, err := st.User("u_demo"); err != nil {
		st.UpsertUser(&domain.User{ID: "u_demo", Name: "Demo User", TrustLevel: "tier0", CreatedAt: time.Now().UTC()})
	}

	key, err := signingKey()
	if err != nil {
		log.Fatalf("signing key: %v", err)
	}

	met := observability.New()
	ve := verifier.New()
	gw := gateway.New(st, key, 15*time.Minute)

	ex := execRouter{
		sim:  executor.SimExecutor{Base: executor.Base{Guard: gw}, BasePrice: 1200},
		http: executor.HTTPExecutor{Base: executor.Base{Guard: gw}},
	}

	sched := scheduler.New(st, planner.RulePlanner{}, ex, gw, ve, met, scheduler.DefaultConfig())
	sched.Notify = func(userID, title, body string) {
		// A real deployment swaps this for push + IM. For the reference build the
		// notification is logged and recorded in the audit chain.
		log.Printf("notify user=%s title=%q body=%q", userID, title, body)
		st.AppendAudit("notifier", "notified", userID, "", "", title+": "+body)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sched.Tick(ctx)
			}
		}
	}()

	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = st.Save()
				return
			case <-t.C:
				if err := st.Save(); err != nil {
					log.Printf("snapshot failed: %v", err)
				}
			}
		}
	}()

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.New(api.Deps{Store: st, Sched: sched, Gateway: gw, Extractor: planner.Extractor{}, Metrics: met}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("agentd listening on %s (data=%s, tick=%s)", addr, dataPath, tick)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = st.Save()
}

// execRouter picks the HTTP adapter when a real URL is present, otherwise the
// simulator. Both satisfy the same Executor interface, so the loop is unchanged.
type execRouter struct {
	sim  executor.Executor
	http executor.Executor
}

func (r execRouter) Execute(ctx context.Context, in *domain.Intent) (*domain.Evidence, error) {
	if in.Params["url"] != "" {
		return r.http.Execute(ctx, in)
	}
	return r.sim.Execute(ctx, in)
}

func signingKey() ([]byte, error) {
	if v := os.Getenv("SIGN_KEY"); v != "" {
		return []byte(v), nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	log.Println("SIGN_KEY not set; generated an ephemeral key (signatures reset on restart)")
	return b, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func durationEnv(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

var _ = hex.EncodeToString
