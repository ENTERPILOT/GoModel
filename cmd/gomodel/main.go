// Package main is the entry point for the LLM gateway server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gomodel/config"
	"gomodel/internal/app"
	"gomodel/internal/observability"
	"gomodel/internal/providers"
	"gomodel/internal/providers/anthropic"
	"gomodel/internal/providers/azure"
	"gomodel/internal/providers/bailian"
	"gomodel/internal/providers/bedrock"
	"gomodel/internal/providers/deepseek"
	"gomodel/internal/providers/gemini"
	"gomodel/internal/providers/groq"
	"gomodel/internal/providers/minimax"
	"gomodel/internal/providers/ollama"
	"gomodel/internal/providers/openai"
	"gomodel/internal/providers/opencodego"
	"gomodel/internal/providers/openrouter"
	"gomodel/internal/providers/oracle"
	"gomodel/internal/providers/vertex"
	"gomodel/internal/providers/vllm"
	"gomodel/internal/providers/xai"
	"gomodel/internal/providers/xiaomi"
	"gomodel/internal/providers/zai"
	"gomodel/internal/version"

	"github.com/joho/godotenv"
)

type lifecycleApp interface {
	Start(ctx context.Context, addr string) error
	Shutdown(ctx context.Context) error
}

var shutdownTimeout = 30 * time.Second

func shutdownApplication(application lifecycleApp, ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		done <- application.Shutdown(ctx)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startApplication calls lifecycleApp.Start and, if Start fails, attempts a
// graceful shutdown via shutdownApplication using shutdownTimeout before
// returning the original start error or a combined start/shutdown error.
func startApplication(application lifecycleApp, addr string) error {
	if err := application.Start(context.Background(), addr); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if shutdownErr := shutdownApplication(application, shutdownCtx); shutdownErr != nil {
			return fmt.Errorf("server failed to start: %w", errors.Join(err, fmt.Errorf("shutdown after start failure: %w", shutdownErr)))
		}
		return err
	}
	return nil
}

// @title          GoModel API
// @version        1.0
// @description    AI gateway routing requests to multiple LLM providers (OpenAI, Anthropic, Gemini, Groq, OpenRouter, DeepSeek, Z.ai, xAI, MiniMax, Xiaomi MiMo, Oracle, Ollama, Bailian). Drop-in OpenAI-compatible API.
// @BasePath       /
// @schemes        http
// @securityDefinitions.apikey BearerAuth
// @in             header
// @name           Authorization
func main() {
	opts, err := parseCLI(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(cliParseExitCode(err))
	}

	if opts.Version {
		fmt.Println(version.Info())
		os.Exit(0)
	}

	_ = godotenv.Load()

	if opts.Health {
		if err := runHealthProbe(opts.HealthTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if err := configureLogging(os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "failed to configure logging: %v\n", err)
		os.Exit(1)
	}

	slog.Info("starting gomodel",
		"version", version.Version,
		"commit", version.Commit,
		"build_date", version.Date,
	)

	result, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	configureSwaggerDocs(result.Config.Server.BasePath)

	factory := providers.NewProviderFactory()

	if result.Config.Metrics.Enabled {
		factory.SetHooks(observability.NewPrometheusHooks())
	}

	factory.Add(openai.Registration)
	factory.Add(openrouter.Registration)
	factory.Add(azure.Registration)
	factory.Add(bailian.Registration)
	factory.Add(oracle.Registration)
	factory.Add(anthropic.Registration)
	factory.Add(bedrock.Registration)
	factory.Add(deepseek.Registration)
	factory.Add(gemini.Registration)
	factory.Add(vertex.Registration)
	factory.Add(groq.Registration)
	factory.Add(minimax.Registration)
	factory.Add(ollama.Registration)
	factory.Add(opencodego.Registration)
	factory.Add(vllm.Registration)
	factory.Add(xai.Registration)
	factory.Add(xiaomi.Registration)
	factory.Add(zai.Registration)

	application, err := app.New(context.Background(), app.Config{
		AppConfig: result,
		Factory:   factory,
	})
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		os.Exit(1)
	}

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit

		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := shutdownApplication(application, ctx); err != nil {
			slog.Error("application shutdown error", "error", err)
		}
	}()

	addr := ":" + result.Config.Server.Port
	if err := startApplication(application, addr); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(1)
	}
}
