package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/sumwai/tokenmp/internal/config"
	"github.com/sumwai/tokenmp/internal/gateway"
	"github.com/sumwai/tokenmp/internal/observability"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是 serve 子命令的入口：读配置、开存储、跑迁移、交给 internal/gateway 装配，
// 并在信号到达时退出。装配细节与运行语义都在 internal/gateway，这里只做参数翻译与
// 退出码映射。
func cmdServe(stderr io.Writer) int {
	cfg, err := config.LoadServe()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "配置错误：%v\n", err)
		return exitFailure
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "启动失败：%v\n", err)
		return exitFailure
	}
	defer func() { _ = st.Close() }()

	// 迁移在启动时执行：表结构随二进制一同发布，启动即对齐，避免新版本对着旧表跑。
	if migrateErr := st.Migrate(ctx); migrateErr != nil {
		_, _ = fmt.Fprintf(stderr, "启动失败：%v\n", migrateErr)
		return exitFailure
	}

	// 信号只决定「何时开始关」，退出码由 gateway.Run 的返回值统一决定。
	// 在装配之前建立：探针采集器与 HTTP 服务共用同一个可取消的上下文。
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw, err := gateway.New(st, gateway.Options{
		CompleteTimeout:             cfg.CompleteTimeout,
		StreamFirstByteTimeout:      cfg.StreamFirstByteTimeout,
		StreamIdleTimeout:           cfg.StreamIdleTimeout,
		UpstreamMaxIdleConns:        cfg.UpstreamMaxIdleConns,
		UpstreamMaxIdleConnsPerHost: cfg.UpstreamMaxIdleConnsPerHost,
		UpstreamIdleConnTimeout:     cfg.UpstreamIdleConnTimeout,
		UsageWriteTimeout:           cfg.UsageWriteTimeout,
		CredentialCooldown:          cfg.CredentialCooldown,
		OAuthRefreshWindow:          cfg.OAuthRefreshWindow,
		OAuthRefreshTimeout:         cfg.OAuthRefreshTimeout,
		RateLimitWait:               cfg.RateLimitWait,
		BreakerThreshold:            cfg.BreakerThreshold,
		BreakerCooldown:             cfg.BreakerCooldown,
		BreakerProbes:               cfg.BreakerProbes,
		ProbeInterval:               cfg.ProbeInterval,
		CredentialLogger:            observability.NewJSONLogger(os.Stdout),
		BreakerLogger:               observability.NewJSONLogger(os.Stdout),
		ProbeLogger:                 observability.NewJSONLogger(os.Stdout),
		Logger:                      observability.NewAccessLogger(os.Stdout),
		Observer:                    observability.NewAttemptObserver(observability.NewJSONLogger(os.Stdout)),
		PluginFiles:                 cfg.PluginFiles,
		PluginLogger:                observability.NewJSONLogger(os.Stdout),
		WebSignupEnabled:            cfg.WebSignupEnabled,
		WebTrustProxy:               cfg.WebTrustProxy,
		WebAuthLogger:               observability.NewJSONLogger(os.Stdout),
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "启动失败：%v\n", err)
		return exitFailure
	}
	defer gw.Close()

	// 探针采集是转发的旁路：启动即开，随信号停止；失败只记日志，不影响 HTTP 服务。
	gw.StartProbes(sigCtx)

	// 用 ListenConfig 而不是裸 net.Listen：监听也接受 context，
	// 使启动阶段的取消与超时有一条统一路径。
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "启动失败：监听 %s 失败：%v\n", cfg.Listen, err)
		return exitFailure
	}

	server := &http.Server{
		Handler:           gw.Handler(),
		ReadHeaderTimeout: gateway.ReadHeaderTimeout,
	}
	if err := gateway.Run(sigCtx, server, ln); err != nil {
		_, _ = fmt.Fprintf(stderr, "运行失败：%v\n", err)
		return exitFailure
	}
	return exitOK
}
