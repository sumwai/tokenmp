package me

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// withIdentity 模拟数据面鉴权中间件写入的身份，使处理器能读到调用方账户。
func withIdentity(id access.Identity, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(access.WithIdentity(r.Context(), id)))
	})
}

// meRequest 发一次请求并读完响应体。
func meRequest(t *testing.T, method, url string) (int, string, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("发送请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), body
}

// TestHandlerRecentParam 覆盖 recent 参数的缺省、封顶与非法取值。
func TestHandlerRecentParam(t *testing.T) {
	tests := []struct {
		query      string
		wantLimit  int
		wantStatus int
	}{
		{query: "", wantLimit: defaultRecent, wantStatus: http.StatusOK},
		{query: "?recent=3", wantLimit: 3, wantStatus: http.StatusOK},
		{query: "?recent=0", wantLimit: 0, wantStatus: http.StatusOK},
		{query: "?recent=99999", wantLimit: maxRecent, wantStatus: http.StatusOK},
		{query: "?recent=-1", wantLimit: 0, wantStatus: http.StatusBadRequest},
		{query: "?recent=abc", wantLimit: 0, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			st := &fakeStore{account: &store.Account{ID: 2, Code: "acct"}}
			server := httptest.NewServer(withIdentity(access.Identity{AccountID: 2, APIKeyID: 7}, NewHandler(New(st, nil))))
			t.Cleanup(server.Close)

			status, contentType, body := meRequest(t, http.MethodGet, server.URL+AccountPath+tt.query)
			if status != tt.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d，响应体 %s", status, tt.wantStatus, body)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			if contentType != access.JSONContentType {
				t.Errorf("Content-Type = %q，期望 %q", contentType, access.JSONContentType)
			}
			if st.recentLimit != tt.wantLimit {
				t.Errorf("传给存储层的条数 = %d，期望 %d", st.recentLimit, tt.wantLimit)
			}
		})
	}
}

// TestHandlerMissingIdentityIsUnauthorized 覆盖未经鉴权中间件时的兜底 401。
func TestHandlerMissingIdentityIsUnauthorized(t *testing.T) {
	server := httptest.NewServer(NewHandler(New(&fakeStore{}, nil)))
	t.Cleanup(server.Close)

	status, _, body := meRequest(t, http.MethodGet, server.URL+AccountPath)
	if status != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401，响应体 %s", status, body)
	}
	var envelope access.ErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("响应体不是统一错误体：%v，原文 %s", err, body)
	}
	if envelope.Error.Code != string(domain.CodeUnauthorized) {
		t.Errorf("错误码 = %q，期望 %q", envelope.Error.Code, domain.CodeUnauthorized)
	}
}

// TestHandlerMethodNotAllowed 覆盖非 GET 方法回 405。
func TestHandlerMethodNotAllowed(t *testing.T) {
	server := httptest.NewServer(withIdentity(access.Identity{AccountID: 2}, NewHandler(New(&fakeStore{}, nil))))
	t.Cleanup(server.Close)

	status, _, body := meRequest(t, http.MethodPost, server.URL+AccountPath)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("状态码 = %d，期望 405，响应体 %s", status, body)
	}
}
