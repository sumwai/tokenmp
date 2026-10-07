package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// TestUpstreamErrorsCarryClass 守护「上游来源的错误必须带类别」。
//
// 为什么值得单独守一条：类别是熔断计数的唯一依据（见 circuit.trippable）。不带类别的错误
// 会被当成「无意见」，于是连接失败与超时静默地停止计入渠道健康度 —— 熔断器慢慢失效，
// 而日志与测试都不会报错。这条用例把那种静默变成一个失败的断言。
//
// 例外只有一处：调用方主动取消归为平台内部错误，刻意不带类别（不是渠道的问题，
// 也不该换渠道重试），见 TestTransportErrorClassification 的对应用例。
func TestUpstreamErrorsCarryClass(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   failure.Class
	}{
		{name: "400 参数错误", status: http.StatusBadRequest, body: `{"error":{"type":"invalid_request_error"}}`, want: failure.ClassRequest},
		{name: "401 凭据失效", status: http.StatusUnauthorized, want: failure.ClassAuth},
		{name: "400 余额不足", status: http.StatusBadRequest, body: `{"error":{"message":"insufficient credits"}}`, want: failure.ClassCredit},
		{name: "429 限流", status: http.StatusTooManyRequests, want: failure.ClassRateLimit},
		{name: "500 上游故障", status: http.StatusInternalServerError, want: failure.ClassUpstream},
		{name: "408 上游超时", status: http.StatusRequestTimeout, want: failure.ClassTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)

			client, err := New(Options{Headers: stubHeaders{}, Adapters: openAIChatAdapters})
			if err != nil {
				t.Fatalf("构造上游客户端失败: %v", err)
			}
			route := domain.Route{UpstreamID: "u1", Protocol: domain.ProtocolOpenAIChat, BaseURL: server.URL}
			_, callErr := client.Complete(context.Background(), route, &domain.Request{Model: "m"}, []byte(`{"model":"m"}`))
			if callErr == nil {
				t.Fatal("非 2xx 上游响应应报错")
			}
			if got := failure.ClassOf(callErr); got != tc.want {
				t.Fatalf("失败类别 = %s，期望 %s", failure.ClassName(got), failure.ClassName(tc.want))
			}
			// 顺带守住「带类别」这件事本身：ClassOf 未带分类时也返回 ClassOther，
			// 所以上面那条断言无法区分「归为 other」与「根本没带类别」。
			if got := failure.ClassNameOf(callErr); got == "" {
				t.Fatal("错误必须携带类别，否则熔断计数会静默失效")
			}
		})
	}
}

// TestCompleteDecodeFailureCarriesUpstreamClass 守护 2xx 报文不可解析时的分类。
//
// 2xx 却给不出可用报文，成因在响应侧（地址写错被重定向、上游用 200 回错误信封）：
// 归上游故障才能换渠道，按「请求有问题」终止会让整条渠道在不健康的状态下继续被选中。
func TestCompleteDecodeFailureCarriesUpstreamClass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html>502 Bad Gateway</html>`))
	}))
	t.Cleanup(server.Close)

	client, err := New(Options{Headers: stubHeaders{}, Adapters: openAIChatAdapters})
	if err != nil {
		t.Fatalf("构造上游客户端失败: %v", err)
	}
	route := domain.Route{UpstreamID: "u1", Protocol: domain.ProtocolOpenAIChat, BaseURL: server.URL}
	_, callErr := client.Complete(context.Background(), route, &domain.Request{Model: "m"}, []byte(`{"model":"m"}`))
	if callErr == nil {
		t.Fatal("2xx 但报文不可解析时应报错")
	}
	if got := failure.ClassOf(callErr); got != failure.ClassUpstream {
		t.Fatalf("失败类别 = %s，期望 upstream", failure.ClassName(got))
	}
	if !failure.ActionsOf(callErr).Has(failure.ActionRetryNextRoute) {
		t.Error("不可解析的上游报文应允许换渠道重试")
	}
}

// TestTransportErrorClassification 守护传输层错误的分类。
//
// 这是「漏标类别」风险最集中的一处：这三条分支不经过状态码分级，全靠构造时显式给出类别。
func TestTransportErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		cancelled bool
		want      failure.Class
		wantTrips bool
	}{
		{
			name:      "deadline 到期归超时",
			err:       context.DeadlineExceeded,
			want:      failure.ClassTimeout,
			wantTrips: true,
		},
		{
			name:      "连接失败归上游故障",
			err:       errors.New("connection refused"),
			want:      failure.ClassUpstream,
			wantTrips: true,
		},
		{
			name:      "调用方主动取消不带类别",
			err:       context.Canceled,
			cancelled: true,
			want:      failure.ClassOther,
			wantTrips: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.cancelled {
				// mapTransportError 只在上下文自身已取消时才归为平台错误：
				// 仅错误链里有 context.Canceled 时，它应该继续走上游故障分支。
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			mapped := mapTransportError(ctx, tc.err)
			if got := failure.ClassOf(mapped); got != tc.want {
				t.Fatalf("失败类别 = %s，期望 %s", failure.ClassName(got), failure.ClassName(tc.want))
			}
			if got := failure.ActionsOf(mapped).Has(failure.ActionCountBreaker); got != tc.wantTrips {
				t.Fatalf("计入熔断 = %v，期望 %v", got, tc.wantTrips)
			}
		})
	}
}
