package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/quota"
)

// 本文件是声明式探针：渠道 config 里 probe 键的解析，以及一次探针调用的取值。
//
// 刻意不为厂商硬编码：探针地址、请求头、metric 到 JSON 路径的映射、窗口都由配置给出。
// 新接一个厂商 = 写一段 config，不改表、不改采集代码。不认识的键照常忽略，
// 配置写坏只让本渠道被跳过，不影响转发。

// defaultProbeMethod 是探针未声明 method 时的请求方法。
const defaultProbeMethod = http.MethodGet

// maxProbeBodyBytes 是探针响应体的读取上限。
//
// 采集只为取值与留档快照，不解析无限流；加一个上限避免上游异常时把内存吃满。
const maxProbeBodyBytes = 1 << 20

// ProbeConfig 是渠道 config 里 probe 键的结构。
type ProbeConfig struct {
	// URL 是探针地址，必填。
	URL string
	// Method 是请求方法，留空取 GET。
	Method string
	// Headers 是随探针发出的请求头；探针端点的鉴权 token 直接写在这里。
	Headers map[string]string
	// Metrics 声明 metric 到 JSON 路径的映射与窗口，至少一条。
	Metrics []ProbeMetric
}

// ProbeMetric 声明一条限额行的采集口径。
//
// Metric + WindowKind + Period 三元组是匹配 upstream_plan_quota 行的键；
// UsedPath 是该窗口已用量在探针响应里的 JSON 路径。
type ProbeMetric struct {
	Metric     billing.Metric
	WindowKind billing.WindowKind
	Period     billing.Period
	UsedPath   string
}

// probeJSON 是 probe 键的 JSON 形态；与 ProbeConfig 分开，让 JSON 标签只在解析处出现。
type probeJSON struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Metrics []struct {
		Metric     string `json:"metric"`
		WindowKind string `json:"window_kind"`
		Period     string `json:"period"`
		UsedPath   string `json:"used_path"`
	} `json:"metrics"`
}

// channelConfigJSON 是 upstream_channel.config 里本网关认识的键。
type channelConfigJSON struct {
	Probe *probeJSON `json:"probe"`
}

// ParseChannelConfig 从渠道 config JSON 里解析探针声明。
//
// 未声明 probe 时返回 (nil, nil)：调用方据此跳过该渠道。声明了但不合法时返回错误，
// 由采集器记结构化日志并跳过，不让一条写坏的配置中断整轮采集。
func ParseChannelConfig(raw []byte) (*ProbeConfig, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var cfg channelConfigJSON
	if err := json.Unmarshal(trimmed, &cfg); err != nil {
		return nil, fmt.Errorf("plan: 渠道 config 不是合法 JSON: %w", err)
	}
	if cfg.Probe == nil {
		return nil, nil
	}
	return cfg.Probe.validate()
}

// validate 把 probe 的 JSON 形态转成解析后的配置，并逐项校验。
func (p probeJSON) validate() (*ProbeConfig, error) {
	config := &ProbeConfig{
		URL:     strings.TrimSpace(p.URL),
		Method:  strings.ToUpper(strings.TrimSpace(p.Method)),
		Headers: p.Headers,
	}
	if config.URL == "" {
		return nil, fmt.Errorf("plan: 探针缺少 url")
	}
	if config.Method == "" {
		config.Method = defaultProbeMethod
	}
	if len(p.Metrics) == 0 {
		return nil, fmt.Errorf("plan: 探针缺少 metrics")
	}
	for i, m := range p.Metrics {
		metric := billing.Metric(strings.TrimSpace(m.Metric))
		if err := billing.ValidateMetric(metric); err != nil {
			return nil, fmt.Errorf("plan: 探针 metrics[%d]: %w", i, err)
		}
		kind := billing.WindowKindFromDB(strings.TrimSpace(m.WindowKind))
		period := billing.PeriodFromDB(strings.TrimSpace(m.Period))
		// 只写周期时由周期推出窗口类型：5h 一定配 rolling，其余自然周期配 calendar。
		// 需要非默认组合时写全 window_kind。
		if kind == "" {
			kind = billing.WindowKindCalendar
			if period == billing.Period5h {
				kind = billing.WindowKindRolling
			}
		}
		if err := quota.ValidWindow(kind, period); err != nil {
			return nil, fmt.Errorf("plan: 探针 metrics[%d]: %w", i, err)
		}
		path := strings.TrimSpace(m.UsedPath)
		if path == "" {
			return nil, fmt.Errorf("plan: 探针 metrics[%d] 缺少 used_path", i)
		}
		config.Metrics = append(config.Metrics, ProbeMetric{
			Metric:     metric,
			WindowKind: kind,
			Period:     period,
			UsedPath:   path,
		})
	}
	return config, nil
}

// ProbeValue 是一次探针取值：某条限额在当前窗口的已用量。
type ProbeValue struct {
	Metric     billing.Metric
	WindowKind billing.WindowKind
	Period     billing.Period
	Used       decimal.Decimal
}

// Prober 执行一次探针调用。
type Prober struct {
	// Client 是探针用的 HTTP 客户端；nil 时用 http.DefaultClient。
	Client *http.Client
}

// Probe 调用探针并把响应映射成各条限额的已用量。
//
// 任一条声明的路径取不到数值都算整体失败：只更新一部分会把「没采到的行」留在旧值上，
// 而快照时间戳已被刷新，路由会拿一份半新半旧的状态做耗尽判定。整体失败时调用方保留旧快照。
func (p Prober) Probe(ctx context.Context, cfg ProbeConfig) ([]byte, []ProbeValue, error) {
	snapshot, err := p.fetch(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	values, err := MapProbe(snapshot, cfg.Metrics)
	if err != nil {
		return nil, nil, err
	}
	return snapshot, values, nil
}

// fetch 发出探针请求并读回响应体，非 2xx 直接报错。
func (p Prober) fetch(ctx context.Context, cfg ProbeConfig) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("plan: 构造探针请求失败: %w", err)
	}
	for name, value := range cfg.Headers {
		req.Header.Set(name, value)
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plan: 探针请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("plan: 读取探针响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("plan: 探针返回状态码 %d", resp.StatusCode)
	}
	return raw, nil
}

// MapProbe 把探针响应映射成各条限额的已用量，是纯函数，便于表驱动单测。
func MapProbe(snapshot []byte, metrics []ProbeMetric) ([]ProbeValue, error) {
	document, err := decodeJSONDocument(snapshot)
	if err != nil {
		return nil, err
	}
	values := make([]ProbeValue, 0, len(metrics))
	for _, m := range metrics {
		used, ok := lookupJSONPath(document, m.UsedPath)
		if !ok {
			return nil, fmt.Errorf("plan: 探针响应里取不到路径 %q", m.UsedPath)
		}
		values = append(values, ProbeValue{
			Metric:     m.Metric,
			WindowKind: m.WindowKind,
			Period:     m.Period,
			Used:       used,
		})
	}
	return values, nil
}
