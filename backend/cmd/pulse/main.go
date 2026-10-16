// Command pulse is the homelab status backend.
//
//	pulse serve                         run the API server
//	pulse token create <name>           mint a user token (prints an enrollment QR)
//	pulse token create --host <host>    mint a host token for /v1/ingest
//	pulse token list
//	pulse token revoke [--host] <name>
//	pulse notify test [message]          send a test notification
//	pulse events purge --source <source> delete a source's events (a retired host)
//
// The token subcommands open the same SQLite file as the server, so in the
// cluster they run through kubectl exec:
//
//	kubectl -n pulse exec -it deploy/pulse -- /pulse token create iphone
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"
	// Embedded zone data: the distroless image has none, and quiet hours
	// are in local time (TZ).
	_ "time/tzdata"

	"github.com/mdp/qrterminal/v3"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/jalenfran/pulse/backend/internal/api"
	"github.com/jalenfran/pulse/backend/internal/kube"
	"github.com/jalenfran/pulse/backend/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "events":
		err = eventsCmd(os.Args[2:])
	case "notify":
		err = notifyCmd(os.Args[2:])
	case "token":
		err = tokenCmd(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pulse:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  pulse serve
  pulse token create <name> | --host <hostname>
  pulse token list
  pulse token revoke [--host] <name>
  pulse notify test [message]
  pulse events purge --source <source>`)
	os.Exit(2)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openStore() (*store.Store, error) {
	return store.Open(env("PULSE_DB", "/data/pulse.db"))
}

// ── serve ──────────────────────────────────────────────────────────────────

func serve() error {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	retentionDays, err := strconv.Atoi(env("PULSE_RETENTION_DAYS", "30"))
	if err != nil || retentionDays < 1 {
		return fmt.Errorf("PULSE_RETENTION_DAYS must be a positive integer")
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	cfg, err := kubeConfig()
	if err != nil {
		return err
	}
	cfg.UserAgent = "pulse"
	cluster, err := kube.New(cfg, st, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	notifier, err := notifierFromEnv(st, log)
	if err != nil {
		return err
	}
	if notifier != nil {
		// Hooked up before the informers start, so nothing they record is missed.
		st.OnEvent(notifier.Consider)
		go notifier.Run(ctx)
	} else {
		log.Info("notifications disabled (PULSE_NTFY_URL unset)")
	}

	// The server comes up immediately so liveness passes; /readyz reports
	// 503 until the caches have synced.
	go func() {
		if err := cluster.Start(ctx); err != nil && ctx.Err() == nil {
			log.Error("informers failed to sync", "err", err)
			stop()
		}
	}()
	go prune(ctx, st, time.Duration(retentionDays)*24*time.Hour, log)

	srv := &http.Server{
		Addr:              env("PULSE_ADDR", ":8080"),
		Handler:           api.New(st, cluster, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: log streams are long-lived by design.
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", srv.Addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// kubeConfig uses the in-cluster service account, falling back to the usual
// kubeconfig for running on a workstation.
func kubeConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, rest.ErrNotInCluster) {
		return nil, err
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
}

func prune(ctx context.Context, st *store.Store, keep time.Duration, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := st.Prune(ctx, time.Now().Add(-keep)); err != nil {
			log.Error("prune events", "err", err)
		} else if n > 0 {
			log.Info("pruned events", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ── token ──────────────────────────────────────────────────────────────────

func tokenCmd(args []string) error {
	if len(args) == 0 {
		usage()
	}
	fs := flag.NewFlagSet("token "+args[0], flag.ExitOnError)
	host := fs.Bool("host", false, "host token (accepted only by /v1/ingest)")
	_ = fs.Parse(args[1:])

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()

	kind := "user"
	if *host {
		kind = "host"
	}

	switch args[0] {
	case "create":
		if fs.NArg() != 1 {
			usage()
		}
		name := fs.Arg(0)
		raw, err := st.CreateToken(ctx, kind, name)
		if err != nil {
			return err
		}
		if kind == "host" {
			fmt.Printf("Host token for %s (shown once):\n\n  %s\n\n", name, raw)
			// POSIX sh, not bash: some hosts' root shell is dash (no <<<).
			fmt.Printf("Install it on %s as root:\n\n  install -d -m 700 /var/lib/pulse\n  printf '%%s\\n' '%s' | install -m 600 /dev/stdin /var/lib/pulse/token\n", name, raw)
			return nil
		}
		fmt.Printf("User token %q (shown once):\n\n  %s\n\n", name, raw)
		publicURL := os.Getenv("PULSE_PUBLIC_URL")
		if publicURL == "" {
			fmt.Println("Set PULSE_PUBLIC_URL to also get an enrollment QR code.")
			return nil
		}
		enroll := "pulse://enroll?" + url.Values{"url": {publicURL}, "token": {raw}}.Encode()
		fmt.Print("Scan to enroll the app:\n\n")
		qrterminal.GenerateHalfBlock(enroll, qrterminal.L, os.Stdout)
		return nil

	case "list":
		tokens, err := st.ListTokens(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tNAME\tCREATED\tLAST USED")
		for _, t := range tokens {
			used := "never"
			if t.LastUsedAt != nil {
				used = t.LastUsedAt.Format(time.DateTime)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Kind, t.Name, t.CreatedAt.Format(time.DateTime), used)
		}
		return tw.Flush()

	case "revoke":
		if fs.NArg() != 1 {
			usage()
		}
		ok, err := st.RevokeToken(ctx, kind, fs.Arg(0))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no %s token named %q", kind, fs.Arg(0))
		}
		fmt.Printf("Revoked %s token %q.\n", kind, fs.Arg(0))
		return nil
	}
	usage()
	return nil
}

// ── events ─────────────────────────────────────────────────────────────────

func eventsCmd(args []string) error {
	if len(args) == 0 || args[0] != "purge" {
		usage()
	}
	fs := flag.NewFlagSet("events purge", flag.ExitOnError)
	source := fs.String("source", "", `source to delete, e.g. "host:old-node"`)
	_ = fs.Parse(args[1:])
	if *source == "" {
		usage()
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.PurgeSource(context.Background(), *source)
	if err != nil {
		return err
	}
	fmt.Printf("Deleted %d events from %s.\n", n, *source)
	return nil
}
