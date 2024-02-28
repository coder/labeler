package main

import (
	"context"
	"crypto/rsa"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/labeler"
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
		appPEMEnv  string
		appID      string
		openAIKey  string
		bindAddr   string
	)
	cmd := &serpent.Cmd{
		Use:   "labeler",
		Short: "labeler is the GitHub labeler backend service",
		Handler: func(inv *serpent.Invocation) error {
			log.Debug("starting labeler")
			if appPEMFile == "" {
				return fmt.Errorf("app-pem-file is required")
			}

			var (
				err    error
				appKey *rsa.PrivateKey
			)
			if appPEMEnv != "" {
				appKey, err = appkey.Parse([]byte(appPEMEnv))
				if err != nil {
					return fmt.Errorf("parse app key: %w", err)
				}
			} else {
				appKey, err = appkey.FromFile(appPEMFile)
				if err != nil {
					return fmt.Errorf("load app key: %w", err)
				}
			}

			appConfig, err := app.NewConfig(appID, appKey)
			if err != nil {
				return fmt.Errorf("create app config: %w", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			openAIKey = strings.TrimSpace(openAIKey)

			oai := openai.NewClient(openAIKey)

			// Validate the OpenAI API key.
			_, err = oai.ListModels(ctx)
			if err != nil {
				return fmt.Errorf("list models: %w", err)
			}

			// support Cloud Run
			port := os.Getenv("PORT")
			if port != "" {
				bindAddr = ":" + port
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

			srv := &labeler.Server{
				Log:       log,
				OpenAI:    oai,
				AppConfig: appConfig,
			}

			srv.Init()

			return http.Serve(listener, srv)
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
				Default:     "843202",
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
				Required:    true,
				Value:       serpent.StringOf(&openAIKey),
			},
			{
				Env:         "GITHUB_APP_PEM",
				Description: "APP PEM in raw form.",
				Value:       serpent.StringOf(&appPEMEnv),
			},
		},
	}

	err := cmd.Invoke().WithOS().Run()
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
