package plan

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

func TestParseChannelConfig(t *testing.T) {
	tests := []struct {
		name    string
		give    string
		wantNil bool
		wantErr string
	}{
		{name: "空配置不是探针", give: "", wantNil: true},
		{name: "JSON null 不是探针", give: "null", wantNil: true},
		{name: "未声明 probe 不是探针", give: `{"signin_header":{"name":"x"}}`, wantNil: true},
		{
			name: "合法探针",
			give: `{"probe":{"url":"https://up/usage","headers":{"Authorization":"Bearer t"},
				"metrics":[{"metric":"input_token","window_kind":"rolling","period":"5h","used_path":"$.usage.5h"}]}}`,
		},
		{name: "探针缺 url", give: `{"probe":{"metrics":[{"metric":"request","period":"day","used_path":"$.used"}]}}`, wantErr: "缺少 url"},
		{name: "探针缺 metrics", give: `{"probe":{"url":"https://up"}}`, wantErr: "缺少 metrics"},
		{
			name:    "未知指标",
			give:    `{"probe":{"url":"https://up","metrics":[{"metric":"not_a_metric","period":"day","used_path":"$.used"}]}}`,
			wantErr: "未知的计量指标",
		},
		{
			name:    "窗口组合不可判定",
			give:    `{"probe":{"url":"https://up","metrics":[{"metric":"request","window_kind":"calendar","period":"5h","used_path":"$.used"}]}}`,
			wantErr: "不是受支持的组合",
		},
		{
			name:    "缺 used_path",
			give:    `{"probe":{"url":"https://up","metrics":[{"metric":"request","period":"day"}]}}`,
			wantErr: "缺少 used_path",
		},
		{name: "config 不是合法 JSON", give: `{`, wantErr: "不是合法 JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseChannelConfig([]byte(tt.give))
			switch {
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("错误 = %v，期望包含 %q", err, tt.wantErr)
				}
			case tt.wantNil:
				if err != nil {
					t.Fatalf("意外错误：%v", err)
				}
				if got != nil {
					t.Fatalf("应当没有探针，得到 %+v", got)
				}
			default:
				if err != nil {
					t.Fatalf("意外错误：%v", err)
				}
				if got == nil {
					t.Fatal("应当解析出探针")
				}
			}
		})
	}
}

func TestParseChannelConfigDefaultsMethod(t *testing.T) {
	cfg, err := ParseChannelConfig([]byte(`{"probe":{"url":"https://up","metrics":[{"metric":"request","period":"day","used_path":"$.used"}]}}`))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if cfg.Method != "GET" {
		t.Errorf("默认 method = %q，期望 GET", cfg.Method)
	}
	if cfg.Metrics[0].WindowKind != billing.WindowKindCalendar || cfg.Metrics[0].Period != billing.PeriodDay {
		t.Errorf("只写周期应推出 calendar/day，得到 %s/%s", cfg.Metrics[0].WindowKind, cfg.Metrics[0].Period)
	}
}

func TestMapProbe(t *testing.T) {
	snapshot := []byte(`{
		"usage": {"5h": 42.5, "requests": "7"},
		"windows": [{"used": 3}, {"used": 9}],
		"empty": null
	}`)
	metrics := []ProbeMetric{
		{Metric: billing.MetricInputToken, WindowKind: billing.WindowKindRolling, Period: billing.Period5h, UsedPath: "$.usage.5h"},
		{Metric: billing.MetricRequest, WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay, UsedPath: "$.usage.requests"},
		{Metric: billing.MetricOutputToken, WindowKind: billing.WindowKindCalendar, Period: billing.PeriodWeek, UsedPath: "$.windows[1].used"},
		{Metric: billing.MetricCacheReadToken, WindowKind: billing.WindowKindCalendar, Period: billing.PeriodMonth, UsedPath: "$.empty"},
	}
	values, err := MapProbe(snapshot, metrics)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	want := []string{"42.5", "7", "9", "0"}
	for i, value := range values {
		if !value.Used.Equal(decimal.RequireFromString(want[i])) {
			t.Errorf("第 %d 条 = %s，期望 %s", i, value.Used.String(), want[i])
		}
	}

	t.Run("路径不存在整体失败", func(t *testing.T) {
		if _, err := MapProbe(snapshot, []ProbeMetric{{UsedPath: "$.usage.missing"}}); err == nil {
			t.Fatal("缺失路径应当报错")
		}
	})
	t.Run("叶子非数值整体失败", func(t *testing.T) {
		if _, err := MapProbe(snapshot, []ProbeMetric{{UsedPath: "$.usage"}}); err == nil {
			t.Fatal("对象叶子应当报错")
		}
	})
	t.Run("响应不是合法 JSON", func(t *testing.T) {
		if _, err := MapProbe([]byte("{"), []ProbeMetric{{UsedPath: "$.a"}}); err == nil {
			t.Fatal("非法 JSON 应当报错")
		}
	})
}

func TestParseJSONPathRejectsMalformed(t *testing.T) {
	for _, path := range []string{"", "usage.5h", "$", "$.", "$.a[", "$.a[-1]", "$.a[x]", "$x"} {
		if _, ok := parseJSONPath(path); ok {
			t.Errorf("路径 %q 应被拒绝", path)
		}
	}
	for _, path := range []string{"$.a", "$.a.b", "$.a[0].b", "$[2]"} {
		if _, ok := parseJSONPath(path); !ok {
			t.Errorf("路径 %q 应被接受", path)
		}
	}
}
