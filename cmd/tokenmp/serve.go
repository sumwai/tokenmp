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

	webauth "github.com/sumwai/tokenmp/internal/auth"
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
		// 邮件通道只在配置了地址时装配：未配置保持 nil，验证码发送入口回 500。
		WebMailer: webauth.NewSMTPMailer(webauth.SMTPConfig{
			Addr:     cfg.SMTPAddr,
			From:     cfg.SMTPFrom,
			User:     cfg.SMTPUser,
			Password: cfg.SMTPPassword,
		}),
		// 第三方登录只装配凭据齐全的提供方；回调地址共用一个配置。
		WebOAuthProviders: buildOAuthProviders(cfg),
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "启动失败：%v\n", err)
		return exitFailure
	}
	defer gw.Close()

	// SIGHUP 表示「重编译中间件」，不进入退出上下文：指纹是「修改时间 + 大小」，
	// cp -p 一类操作看不出变化，这是不依赖指纹的兜底。Windows 下该信号不会到达。
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for range hup {
			gw.ReloadPlugins()
		}
	}()

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

// 第三方登录的端点常量：写在装配处而不是能力包里，
// 能力包只认字段，提供方的 URL 演进不牵动内部实现。
const (
	oauthGoogleAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	oauthGoogleTokenURL     = "https://oauth2.googleapis.com/token" //nolint:gosec // G101：这是公开端点 URL
	oauthGoogleUserInfoURL  = "https://openidconnect.googleapis.com/v1/userinfo"
	oauthGoogleScope        = "openid email profile"

	oauthGitHubAuthorizeURL = "https://github.com/login/oauth/authorize"
	oauthGitHubTokenURL     = "https://github.com/login/oauth/access_token" //nolint:gosec // G101：这是公开端点 URL
	oauthGitHubUserInfoURL  = "https://api.github.com/user"
	oauthGitHubEmailsURL    = "https://api.github.com/user/emails"
	oauthGitHubScope        = "read:user user:email"
)

// buildOAuthProviders 组装凭据齐全的提供方；ID 或 secret 任一为空即不启用。
func buildOAuthProviders(cfg config.Serve) []webauth.OAuthProvider {
	var providers []webauth.OAuthProvider
	if cfg.OAuthGoogleClientID != "" && cfg.OAuthGoogleClientSecret != "" {
		providers = append(providers, webauth.OAuthProvider{
			ID: "google", Name: "Google",
			ClientID: cfg.OAuthGoogleClientID, ClientSecret: cfg.OAuthGoogleClientSecret,
			RedirectURI:  cfg.OAuthRedirectURI,
			AuthorizeURL: oauthGoogleAuthorizeURL, TokenURL: oauthGoogleTokenURL,
			UserInfoURL: oauthGoogleUserInfoURL, Scope: oauthGoogleScope,
		})
	}
	if cfg.OAuthGitHubClientID != "" && cfg.OAuthGitHubClientSecret != "" {
		providers = append(providers, webauth.OAuthProvider{
			ID: "github", Name: "GitHub",
			ClientID: cfg.OAuthGitHubClientID, ClientSecret: cfg.OAuthGitHubClientSecret,
			RedirectURI:  cfg.OAuthRedirectURI,
			AuthorizeURL: oauthGitHubAuthorizeURL, TokenURL: oauthGitHubTokenURL,
			UserInfoURL: oauthGitHubUserInfoURL, EmailsURL: oauthGitHubEmailsURL,
			Scope: oauthGitHubScope,
		})
	}
	return providers
}
