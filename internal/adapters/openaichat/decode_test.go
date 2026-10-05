package openaichat

import "testing"

// TestDecodeRequestUsageFramesRequested 守护「客户端是否索取流式用量帧」的解析口径：
// 只认 stream_options.include_usage 为 true；缺失、形状非对象、取值非布尔都按未索取处理，
// 不因为一个可选开关把整条请求判为失败。
func TestDecodeRequestUsageFramesRequested(t *testing.T) {
	base := `"model":"alias","messages":[{"role":"user","content":"hi"}],"stream":true`
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "未给开关",
			body: `{` + base + `}`,
			want: false,
		},
		{
			name: "开关为真",
			body: `{` + base + `,"stream_options":{"include_usage":true}}`,
			want: true,
		},
		{
			name: "开关为假",
			body: `{` + base + `,"stream_options":{"include_usage":false}}`,
			want: false,
		},
		{
			name: "开关为真且带其它选项",
			body: `{` + base + `,"stream_options":{"include_usage":true,"foo":"bar"}}`,
			want: true,
		},
		{
			name: "stream_options 为 null",
			body: `{` + base + `,"stream_options":null}`,
			want: false,
		},
		{
			name: "stream_options 为数组",
			body: `{` + base + `,"stream_options":[]}`,
			want: false,
		},
		{
			name: "开关取值非布尔",
			body: `{` + base + `,"stream_options":{"include_usage":"true"}}`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := New().DecodeRequest([]byte(tt.body))
			if err != nil {
				t.Fatalf("解码失败：%v", err)
			}
			if req.UsageFramesRequested != tt.want {
				t.Errorf("UsageFramesRequested = %v，期望 %v", req.UsageFramesRequested, tt.want)
			}
		})
	}
}
