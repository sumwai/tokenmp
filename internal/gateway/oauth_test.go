package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/oauth"
)

// 本文件覆盖续期器：成功写回新令牌、invalid_grant 落过期标记、写回失败不阻断本次请求。

// recordingSecretWriter 记录续期写回。
type recordingSecretWriter struct {
	id     uint64
	secret []byte
	err    error
	calls  int
}

func (w *recordingSecretWriter) UpdateCredentialSecret(_ context.Context, id uint64, secret []byte) error {
	w.calls++
	w.id = id
	w.secret = append([]byte(nil), secret...)
	return w.err
}

// testRefreshInput 是续期器用例共用的输入。
func testRefreshInput(profile domain.OAuthProfile) credential.OAuthRefreshInput {
	return credential.OAuthRefreshInput{
		CredentialID: 5,
		Name:         "primary",
		Profile:      profile,
		Access:       "access-old",
		Refresh:      "refresh-1",
		Expires:      time.Unix(1_700_000_000, 0),
		Account:      "acct-1",
	}
}

// TestOAuthRefresherWritesBackNewTokens 断言续期成功后把新令牌写回凭据行。
func TestOAuthRefresherWritesBackNewTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"access-new","refresh_token":"refresh-new","expires_in":3600}`))
	}))
	defer server.Close()

	store := &recordingSecretWriter{}
	refresher := &oauthCredentialRefresher{client: oauth.New(oauth.Options{HTTPClient: server.Client()}), store: store}
	out, err := refresher.Refresh(context.Background(), testRefreshInput(domain.OAuthProfile{
		TokenURL: server.URL,
		ClientID: "client-1",
	}))
	if err != nil {
		t.Fatalf("续期失败：%v", err)
	}
	if out.Access != "access-new" || out.Refresh != "refresh-new" {
		t.Fatalf("续期结果不符：%+v", out)
	}
	if store.calls != 1 || store.id != 5 {
		t.Fatalf("写回不符：calls=%d id=%d", store.calls, store.id)
	}
	parsed, err := credential.ParseSecret(store.secret)
	if err != nil {
		t.Fatalf("写回的 secret 无法解析：%v", err)
	}
	if parsed.Access != "access-new" || parsed.Refresh != "refresh-new" || parsed.Account != "acct-1" {
		t.Fatalf("写回的 secret 不符：%+v", parsed)
	}
}

// TestOAuthRefresherInvalidGrantMarksExpired 断言 invalid_grant 时写回过期标记并返回原错误。
func TestOAuthRefresherInvalidGrantMarksExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer server.Close()

	store := &recordingSecretWriter{}
	refresher := &oauthCredentialRefresher{client: oauth.New(oauth.Options{HTTPClient: server.Client()}), store: store}
	_, err := refresher.Refresh(context.Background(), testRefreshInput(domain.OAuthProfile{
		TokenURL: server.URL,
		ClientID: "client-1",
	}))
	if !errors.Is(err, oauth.ErrInvalidGrant) {
		t.Fatalf("应返回 ErrInvalidGrant，得到 %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("应写回过期标记，实际写回 %d 次", store.calls)
	}
	parsed, parseErr := credential.ParseSecret(store.secret)
	if parseErr != nil {
		t.Fatalf("过期标记无法解析：%v", parseErr)
	}
	if parsed.Kind != credential.KindOAuth || parsed.Access != "" {
		t.Fatalf("过期标记不符：%+v", parsed)
	}
	if !parsed.Expires.Equal(time.UnixMilli(0)) {
		t.Fatalf("过期标记的时刻应为 epoch，得到 %s", parsed.Expires)
	}
}

// TestOAuthRefresherWriteBackFailureStillReturnsToken 断言写回失败不阻断本次请求。
func TestOAuthRefresherWriteBackFailureStillReturnsToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"access-new","refresh_token":"refresh-new","expires_in":3600}`))
	}))
	defer server.Close()

	store := &recordingSecretWriter{err: errors.New("数据库不可写")}
	refresher := &oauthCredentialRefresher{client: oauth.New(oauth.Options{HTTPClient: server.Client()}), store: store}
	out, err := refresher.Refresh(context.Background(), testRefreshInput(domain.OAuthProfile{
		TokenURL: server.URL,
		ClientID: "client-1",
	}))
	if err != nil {
		t.Fatalf("写回失败不应让续期失败：%v", err)
	}
	if out.Access != "access-new" {
		t.Fatalf("应返回新令牌，得到 %+v", out)
	}
}
