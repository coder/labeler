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

	"cloud.google.com/go/compute/metadata"
	"github.com/beatlabs/github-auth/app"
	appkey "github.com/beatlabs/github-auth/key"
	"github.com/coder/labeler"
	"github.com/coder/serpent"
	"github.com/jussi-kalliokoski/slogdriver"
	"github.com/lmittmann/tint"
	"github.com/sashabaranov/go-openai"
)

func newLogger() *slog.Logger {
	gcpProjectID, err := metadata.ProjectID()
	if err != nil {
		logOpts := &tint.Options{
			AddSource:  true,
			Level:      slog.LevelDebug,
			TimeFormat: time.Kitchen + " 05.999",
		}
		return slog.New(tint.NewHandler(os.Stderr, logOpts))
	}

	return slog.New(
		slogdriver.NewHandler(
			os.Stderr,
			slogdriver.Config{
				ProjectID: gcpProjectID,
				Level:     slog.LevelDebug,
			},
		),
	)
}

type rootCmd struct {
	appPEMFile  string
	appPEMEnv   string
	appID       string
	openAIKey   string
	openAIModel string
	bindAddr    string
}

func (r *rootCmd) appConfig() (*app.Config, error) {
	var (
		err    error
		appKey *rsa.PrivateKey
	)
	if r.appPEMEnv != "" {
		appKey, err = appkey.Parse([]byte(r.appPEMEnv))
		if err != nil {
			return nil, fmt.Errorf("parse app key: %w", err)
		}
	} else {
		appKey, err = appkey.FromFile(r.appPEMFile)
		if err != nil {
			return nil, fmt.Errorf("load app key: %w", err)
		}
	}

	appConfig, err := app.NewConfig(r.appID, appKey)
	if err != nil {
		return nil, fmt.Errorf("create app config: %w", err)
	}

	return appConfig, nil
}

func (r *rootCmd) ai(ctx context.Context) (*openai.Client, error) {
	openAIKey := strings.TrimSpace(r.openAIKey)

	oai := openai.NewClient(openAIKey)

	// Validate the OpenAI API key.
	_, err := oai.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}

	return openai.NewClient(openAIKey), nil
}

func main() {
	log := newLogger()
	var root rootCmd
	cmd := &serpent.Cmd{
		Use:   "labeler",
		Short: "labeler is the GitHub labeler backend service",
		Children: []*serpent.Cmd{
			root.testCmd(),
		},
		Handler: func(inv *serpent.Invocation) error {
			log.Debug("starting labeler")
			if root.appPEMFile == "" {
				return fmt.Errorf("app-pem-file is required")
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			oai, err := root.ai(ctx)
			if err != nil {
				return fmt.Errorf("openai: %w", err)
			}

			appConfig, err := root.appConfig()
			if err != nil {
				return fmt.Errorf("app config: %w", err)
			}

			bindAddr := root.bindAddr
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

			srv := &labeler.Service{
				Log:       log,
				OpenAI:    oai,
				Model:     root.openAIModel,
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
				Value:       serpent.StringOf(&root.appPEMFile),
			},
			{
				Flag:        "app-id",
				Default:     "843202",
				Description: "GitHub App ID.",
				Required:    true,
				Value:       serpent.StringOf(&root.appID),
			},
			{
				Flag:        "bind-addr",
				Description: "Address to bind to.",
				Default:     "localhost:8080",
				Value:       serpent.StringOf(&root.bindAddr),
			},
			{
				Flag:        "openai-model",
				Default:     openai.GPT4TurboPreview,
				Description: "OpenAI model to use.",
				Value:       serpent.StringOf(&root.openAIModel),
			},
			// SECRETS: only configurable via environment variables.
			{
				Description: "OpenAI API key.",
				Env:         "OPENAI_API_KEY",
				Required:    true,
				Value:       serpent.StringOf(&root.openAIKey),
			},
			{
				Env:         "GITHUB_APP_PEM",
				Description: "APP PEM in raw form.",
				Value:       serpent.StringOf(&root.appPEMEnv),
			},
		},
	}

	err := cmd.Invoke().WithOS().Run()
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
