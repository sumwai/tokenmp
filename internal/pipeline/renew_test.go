package pipeline

import (
	"context"
	"testing"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/domain"
)

// credentialRenewedError 模拟上游在非 2xx 响应上标注「凭据登录态已续期」的错误。
type credentialRenewedError struct{}

func (credentialRenewedError) Error() string { return "凭据登录态已续期" }

func (credentialRenewedError) CredentialRenewed() bool { return true }

// TestForwardRenewsCredentialOnRenewedResult 断言 2xx 结果里声明的续期事实会解除凭据冷却。
func TestForwardRenewsCredentialOnRenewedResult(t *testing.T) {
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		return &domain.UpstreamResult{
			Raw:               []byte(`{"ok":true}`),
			Response:          &domain.Response{},
			CredentialRenewed: true,
		}, nil
	}}
	rotation := &fakeRotation{}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Credentials: rotation,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err != nil {
		t.Fatalf("转发应成功：%v", err)
	}
	if rotation.renewed != 1 {
		t.Fatalf("续期调用次数 = %d，期望 1", rotation.renewed)
	}
}

// TestForwardRenewsCredentialOnRenewedError 断言失败响应上声明的续期事实同样解除冷却，
// 且不触发换凭据。
func TestForwardRenewsCredentialOnRenewedError(t *testing.T) {
	caller := fakeCaller{complete: func(context.Context, domain.Route, *domain.Request, []byte) (*domain.UpstreamResult, error) {
		return nil, credentialRenewedError{}
	}}
	rotation := &fakeRotation{}
	adapter := openaichat.New()
	p, err := New(Options{
		Adapters:    chatAdapterLookup(adapter),
		Upstream:    caller,
		Routes:      fakeRouteResolver{routes: []domain.Route{chatRoute()}},
		Credentials: rotation,
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}

	if err := p.Forward(context.Background(), adapter, newChatRequest(false), &recordingWriter{events: &[]string{}}); err == nil {
		t.Fatal("续期响应本身不是成功响应，应把上游错误返回客户端")
	}
	if rotation.renewed != 1 {
		t.Fatalf("续期调用次数 = %d，期望 1", rotation.renewed)
	}
	if rotation.advanced != 0 {
		t.Errorf("续期不应触发换凭据，实际切换 %d 次", rotation.advanced)
	}
}
