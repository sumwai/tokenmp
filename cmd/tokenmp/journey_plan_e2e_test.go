//go:build e2e

// 上游套餐与配额的端到端验证：mock 探针报满额 → 路由跳过该渠道由候选 B 服务 →
// 快照过期放行 → 窗口重置后恢复 → 探针不可达时转发不受影响。
//
// 单独成文件而不塞进主剧本：本步骤自带 mock 探针与两个新候选渠道，状态自成一体；
// 主剧本只在末尾追加一行调用。
package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/plan"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本步骤使用的模型别名与上游模型名。
const (
	e2ePlanModel         = "e2e-plan"
	e2ePlanUpstreamModel = "up-e2e-plan"
)

// 两个候选渠道各自的上游凭据，用于断言最终由哪条渠道服务。
const (
	e2ePlanAKey = "e2e-upstream-plan-a"
	e2ePlanBKey = "e2e-upstream-plan-b"
)

// e2ePlanQuotaLimit 是套餐限额：单次请求输入 token 数为 12，故取 10 即一次调用后即满。
const e2ePlanQuotaLimit = "10"

// e2ePlanProbe 是进程内 mock 探针：返回值由测试随时改，用来模拟窗口用量与窗口重置。
type e2ePlanProbe struct {
	server *httptest.Server
	url    string

	mu   sync.Mutex
	used string
}

// newE2EPlanProbe 起一个 mock 探针并注册关闭。
func newE2EPlanProbe(t *testing.T) *e2ePlanProbe {
	t.Helper()
	probe := &e2ePlanProbe{used: "0"}
	probe.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probe.mu.Lock()
		used := probe.used
		probe.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fmt.Sprintf(`{"used":%s}`, used))
	}))
	probe.url = probe.server.URL
	t.Cleanup(probe.server.Close)
	return probe
}

// setUsed 设置探针下一次返回的窗口已用量。
func (p *e2ePlanProbe) setUsed(used string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used = used
}

// stop 关闭 mock 探针，模拟探针不可达。
func (p *e2ePlanProbe) stop() { p.server.Close() }

// e2ePlanProbeConfig 生成渠道 config 里的探针声明。
func e2ePlanProbeConfig(url string) string {
	return fmt.Sprintf(
		`{"probe":{"url":%q,"metrics":[{"metric":"input_token","window_kind":"rolling","period":"5h","used_path":"$.used"}]}}`,
		url)
}

// step9UpstreamPlan 覆盖上游套餐配额的路由消费。
func (j *e2eJourney) step9UpstreamPlan(t *testing.T) {
	ctx := j.ctx

	probe := newE2EPlanProbe(t)
	probe.setUsed("0")

	const planGroup = "e2e-plan-group"
	planID, err := j.svc.CreatePlan(ctx, admin.PlanInput{
		MerchantID: j.merchantID,
		CredGroup:  planGroup,
		Name:       "E2E 套餐",
		Quotas: []admin.PlanQuotaInput{{
			Metric:      billing.MetricInputToken,
			WindowKind:  billing.WindowKindRolling,
			Period:      billing.Period5h,
			LimitAmount: e2ePlanQuotaLimit,
		}},
	})
	e2eMust(t, err)

	// 候选 A：优先级更高，声明探针，套餐挂在它的 cred_group 上。
	channelA, err := j.svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: j.merchantID, Name: "e2e-plan-a", Vendor: "fake",
		Type: store.ChannelTypeOpenAIChat, CredGroup: planGroup,
		BaseURL: j.upstreamURL, Priority: 200,
		Config: e2ePlanProbeConfig(probe.url),
	})
	e2eMust(t, err)
	_, err = j.svc.AddCredential(ctx, admin.CredentialInput{
		MerchantID: j.merchantID, Group: planGroup, Name: "primary", APIKey: e2ePlanAKey,
	})
	e2eMust(t, err)
	_, err = j.svc.SetModelMap(ctx, admin.ModelMapInput{
		ChannelID: channelA, Model: e2ePlanModel, UpstreamModel: e2ePlanUpstreamModel, PriceMultiplier: "1",
	})
	e2eMust(t, err)

	// 候选 B：优先级更低，无探针，A 被跳过时的兜底。
	const fallbackGroup = "e2e-plan-fallback"
	channelB, err := j.svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: j.merchantID, Name: "e2e-plan-b", Vendor: "fake",
		Type: store.ChannelTypeOpenAIChat, CredGroup: fallbackGroup,
		BaseURL: j.upstreamURL, Priority: 100,
	})
	e2eMust(t, err)
	_, err = j.svc.AddCredential(ctx, admin.CredentialInput{
		MerchantID: j.merchantID, Group: fallbackGroup, Name: "primary", APIKey: e2ePlanBKey,
	})
	e2eMust(t, err)
	_, err = j.svc.SetModelMap(ctx, admin.ModelMapInput{
		ChannelID: channelB, Model: e2ePlanModel, UpstreamModel: e2ePlanUpstreamModel, PriceMultiplier: "1",
	})
	e2eMust(t, err)

	// 定价：让本次调用走完整结算路径，与其它步骤同口径。
	_, err = j.svc.PublishPricing(ctx, admin.PublishPricingInput{
		MerchantID: j.merchantID, Model: e2ePlanUpstreamModel,
		EffectiveAt: time.Now().Add(-time.Minute), Components: e2ePricingComponents(),
	})
	e2eMust(t, err)

	collect := func() {
		t.Helper()
		collector, err := plan.NewCollector(j.st, plan.CollectorOptions{Interval: time.Minute})
		e2eMust(t, err)
		collector.CollectOnce(ctx)
	}
	endpoint := j.gatewayURL + domain.ProtocolOpenAIChat.EndpointPath()
	request := e2eRequest(domain.ProtocolOpenAIChat, e2ePlanModel, false)
	call := func(step string) {
		t.Helper()
		result := e2ePost(t, endpoint, j.mainKey, request)
		if result.status != http.StatusOK {
			t.Fatalf("%s 状态码 = %d，期望 200，响应体 %s", step, result.status, result.body)
		}
	}

	// 1) 配额未满：路由走 A。
	collect()
	aBefore, bBefore := j.upstream.keyHit(e2ePlanAKey), j.upstream.keyHit(e2ePlanBKey)
	call("配额未满")
	if j.upstream.keyHit(e2ePlanAKey) != aBefore+1 {
		t.Fatalf("配额未满时应由渠道 A 服务：A 命中 %d→%d", aBefore, j.upstream.keyHit(e2ePlanAKey))
	}
	if j.upstream.keyHit(e2ePlanBKey) != bBefore {
		t.Fatalf("配额未满时不应调用渠道 B：B 命中 %d→%d", bBefore, j.upstream.keyHit(e2ePlanBKey))
	}

	// 2) 探针报满额且快照新鲜：跳过 A，由 B 服务。
	probe.setUsed(e2ePlanQuotaLimit)
	collect()
	aBefore, bBefore = j.upstream.keyHit(e2ePlanAKey), j.upstream.keyHit(e2ePlanBKey)
	call("配额耗尽")
	if j.upstream.keyHit(e2ePlanAKey) != aBefore {
		t.Fatalf("配额耗尽时不应调用渠道 A：A 命中 %d→%d", aBefore, j.upstream.keyHit(e2ePlanAKey))
	}
	if j.upstream.keyHit(e2ePlanBKey) != bBefore+1 {
		t.Fatalf("配额耗尽时应由渠道 B 服务：B 命中 %d→%d", bBefore, j.upstream.keyHit(e2ePlanBKey))
	}

	// 3) 快照超过两个采集周期：按未知放行 A，宁可尝试不可误杀。
	if _, err := j.st.DB().ExecContext(ctx,
		"UPDATE upstream_plan SET last_checked_at = ? WHERE id = ?", time.Now().Add(-time.Hour), planID); err != nil {
		t.Fatalf("回拨套餐采集时刻失败：%v", err)
	}
	aBefore = j.upstream.keyHit(e2ePlanAKey)
	call("快照过期")
	if j.upstream.keyHit(e2ePlanAKey) != aBefore+1 {
		t.Fatalf("快照过期时应放行渠道 A：A 命中 %d→%d", aBefore, j.upstream.keyHit(e2ePlanAKey))
	}

	// 4) 模拟上游窗口重置：探针报 0，重新采集后 A 恢复。
	probe.setUsed("0")
	collect()
	aBefore = j.upstream.keyHit(e2ePlanAKey)
	call("窗口重置后")
	if j.upstream.keyHit(e2ePlanAKey) != aBefore+1 {
		t.Fatalf("窗口重置后应由渠道 A 服务：A 命中 %d→%d", aBefore, j.upstream.keyHit(e2ePlanAKey))
	}

	// 5) 探针不可达：采集失败保留旧快照，转发完全不受影响。
	probe.stop()
	collect()
	call("探针不可达")

	// 套餐回读：限额行停留在最近一次成功采集的用量上，未被失败采集改写。
	views, err := j.svc.ListPlans(ctx)
	e2eMust(t, err)
	found := false
	for _, view := range views {
		if view.ID != planID {
			continue
		}
		found = true
		if len(view.Quotas) != 1 {
			t.Fatalf("套餐限额行数 = %d，期望 1", len(view.Quotas))
		}
		if !e2eDecimalEqual(view.Quotas[0].LastUsed, "0") {
			t.Errorf("探针失败不应改写已用量，得到 %s", view.Quotas[0].LastUsed)
		}
		if view.Quotas[0].UsedPercent == nil {
			t.Error("已用百分比应当可判定")
		}
	}
	if !found {
		t.Fatalf("套餐列表里找不到套餐 %d", planID)
	}
}
