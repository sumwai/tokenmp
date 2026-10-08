package gateway

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/plugin"
	"github.com/sumwai/tokenmp/internal/store"
)

// TestGatewayWarnsWhenMixedFrameSkipsMiddleware 守护「混帧绕过中间件」在装配好的网关里可见。
//
// 检测点在流水线、日志出口在插件层，中间隔着两次注入。上游把 finish_reason 挂在最后一个
// 内容帧上，解码后同帧产出 [TextDelta, ChunkFinish]，中间件因此整体不介入该帧：客户端仍应
// 拿到逐字节透传的原始帧，而日志里必须出现那条告警 —— 否则「插件对这个渠道不生效」与
// 「没导出钩子」在生产上无从区分，这正是本条要闭合的缺口。
func TestGatewayWarnsWhenMixedFrameSkipsMiddleware(t *testing.T) {
	dir := t.TempDir()
	middlewarePath := writePluginFile(t, dir, "events.mw.js",
		"export const events = [\"text_delta\"];\nexport function onEvent(chunk) { return chunk; }\n")
	stateFile := filepath.Join(dir, "plugins.json")
	if err := plugin.SaveRegistry(stateFile, &plugin.Registry{Plugins: []plugin.Entry{
		{Name: "events.mw.js", Path: middlewarePath, Enabled: true},
	}}); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}

	// 上游流：内容与 finish_reason 同帧，随后是终止标记。
	const mixedFrames = "data: {\"id\":\"chatcmpl-1\",\"model\":\"up-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	upstream, _ := newUpstreamRecorder(t, mixedFrames)

	var logs syncBuffer
	st := &fakeGatewayStore{
		auth: activeAuth(),
		routes: []store.RouteCandidate{{
			ChannelID: 10, BaseURL: upstream.URL, CredGroup: "group-a", UpstreamModel: "up-model",
		}},
		credentials:    []store.Credential{{Name: "default", Secret: []byte(`{"api_key":"sk-upstream"}`)}},
		wantMerchantID: 1,
	}
	gw, err := newGateway(st, gatewayOptions{
		CompleteTimeout: 5 * time.Second,
		PluginStateFile: stateFile,
		PluginLogger:    slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("装配网关失败：%v", err)
	}
	t.Cleanup(gw.Close)
	server := httptest.NewServer(gw.handler)
	t.Cleanup(server.Close)

	result := doPost(t, server.URL+domain.ProtocolOpenAIChat.EndpointPath(), authSchemePrefix+testAPIKey,
		`{"model":"alias","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if string(result.body) != mixedFrames {
		t.Errorf("混帧应逐字节透传：\n实际 %s\n期望 %s", result.body, mixedFrames)
	}
	if !strings.Contains(logs.String(), "中间件未介入该帧") {
		t.Fatalf("混帧绕过中间件时应在插件日志里留下告警，实际日志：%s", logs.String())
	}
}
