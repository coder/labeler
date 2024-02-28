package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/coder/ai"
	"github.com/coder/ai/aid"
	"github.com/coder/ai/aid/auth"
	"github.com/coder/serpent"
	"github.com/lmittmann/tint"
	"github.com/sashabaranov/go-openai"

	"github.com/beatlabs/github-auth/app"
	appkey "github.com/beatlabs/github-auth/key"
)

func newLogger() *slog.Logger {
	logOpts := &tint.Options{
		AddSource:  true,
		Level:      slog.LevelDebug,
		TimeFormat: time.Kitchen + " 05.999",
	}
	return slog.New(tint.NewHandler(os.Stderr, logOpts))
}

func main() {
	log := newLogger()
	var (
		appPEMFile string
		appID      string
		openAIKey  string
		bindAddr   string
	)
	cmd := &serpent.Cmd{
		Use:   "aid",
		Short: "aid is a long-running service that processes GitHub data",
		Handler: func(inv *serpent.Invocation) error {
			log.Debug("starting aid")
			if appPEMFile == "" {
				return fmt.Errorf("app-pem-file is required")
			}

			appKey, err := appkey.FromFile(appPEMFile)
			if err != nil {
				return fmt.Errorf("load app key: %w", err)
			}

			appConfig, err := app.NewConfig(appID, appKey)
			if err != nil {
				return fmt.Errorf("create app config: %w", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			oai := openai.NewClient(openAIKey)

			idx := &aid.Indexer{
				Log:        log,
				AppConfig:  appConfig,
				Redis:      redis,
				OpenAI:     oai,
				RepoFilter: regexp.MustCompile(repoFilter),
				OrgFilter:  regexp.MustCompile(orgFilter),
			}

			listener, err := net.Listen("tcp", bindAddr)
			if err != nil {
				return fmt.Errorf("listen: %w", err)
			}
			log.Info("listening", "addr", listener.Addr())

			go func() {
				<-ctx.Done()
				listener.Close()
			}()

			srv := &aid.APIServer{
				Authorizer: &auth.GitHub{
					AppConfig:    appConfig,
					ClientID:     githubProviderID,
					ClientSecret: githubProviderSecret,
					ExternalURL:  externalURL,
				},
				WebFrontend: &ai.WebFrontend{
					ProxyURL: &frontendURL,
				},
				Log:    log,
				OpenAI: oai,
				Redis:  redis,
			}

			srv.Init()

			go func() {
				defer cancel()

				err = http.Serve(listener, srv)
				if err != nil {
					log.Error("http server", "error", err)
				}
			}()

			idxTicker := time.NewTicker(5 * time.Minute)
			defer idxTicker.Stop()
			for {
				err := idx.Run(ctx)
				if err != nil {
					log.Error("indexer run", "error", err)
				}

				select {
				case <-ctx.Done():
					return nil
				case <-idxTicker.C:
					continue
				}
			}
		},
		Options: []serpent.Option{
			{
				Flag:        "app-pem-file",
				Default:     "./app.pem",
				Description: "Path to the GitHub App PEM file.",
				Required:    true,
				Value:       serpent.StringOf(&appPEMFile),
			},
			{
				Flag:        "app-id",
				Default:     "352584",
				Description: "GitHub App ID.",
				Required:    true,
				Value:       serpent.StringOf(&appID),
			},
			{
				Flag:        "bind-addr",
				Description: "Address to bind to.",
				Default:     "localhost:8080",
				Value:       serpent.StringOf(&bindAddr),
			},

			// SECRETS: only configurable via environment variables.
			{
				Description: "OpenAI API key.",
				Env:         "OPENAI_API_KEY",
				Value:       serpent.StringOf(&openAIKey),
			},
		},
	}

	err := cmd.Invoke().WithOS().Run()
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
