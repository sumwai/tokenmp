package billing

import (
	"reflect"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

func TestUsageFromDomainMapsEveryComponent(t *testing.T) {
	usage := domain.Usage{
		Source:           domain.UsageSourceUpstream,
		InputTokens:      100,
		OutputTokens:     40,
		CacheReadTokens:  30,
		CacheWriteTokens: 10,
		ReasoningTokens:  7,
	}
	want := map[Metric]int{
		MetricInputToken:      100,
		MetricOutputToken:     40,
		MetricCacheReadToken:  30,
		MetricCacheWriteToken: 10,
		MetricReasoningToken:  7,
	}
	got, dropped, conflicts := UsageFromDomain(usage)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分量 = %#v，期望 %#v", got, want)
	}
	if len(dropped) != 0 {
		t.Errorf("不应有被丢弃的分量，得到 %v", dropped)
	}
	if len(conflicts) != 0 {
		t.Errorf("不应有口径冲突，得到 %v", conflicts)
	}
}

// TestUsageFromDomainSkipsZeroComponents 验证零值分量不写进 JSON：
// 流水行只保留上游确实陈述过的分量，形状不随上游是否给出子项漂移。
func TestUsageFromDomainSkipsZeroComponents(t *testing.T) {
	usage := domain.Usage{
		Source:       domain.UsageSourceUpstream,
		InputTokens:  5,
		OutputTokens: 2,
	}
	got, dropped, conflicts := UsageFromDomain(usage)
	want := map[Metric]int{MetricInputToken: 5, MetricOutputToken: 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分量 = %#v，期望 %#v", got, want)
	}
	if len(dropped) != 0 {
		t.Errorf("不应有被丢弃的分量，得到 %v", dropped)
	}
	if len(conflicts) != 0 {
		t.Errorf("不应有口径冲突，得到 %v", conflicts)
	}
}

// TestUsageFromDomainDropsUnmappedComponent 验证没有对应 Metric 的分量被丢弃并报告，
// 而不是让整行流水落不下来。
func TestUsageFromDomainDropsUnmappedComponent(t *testing.T) {
	usage := domain.Usage{
		Source:         domain.UsageSourceUpstream,
		InputTokens:    5,
		ServerToolUses: 3,
	}
	got, dropped, _ := UsageFromDomain(usage)
	want := map[Metric]int{MetricInputToken: 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分量 = %#v，期望 %#v", got, want)
	}
	if !reflect.DeepEqual(dropped, []string{"server_tool_uses"}) {
		t.Errorf("丢弃分量 = %v，期望 [server_tool_uses]", dropped)
	}
}

// TestUsageFromDomainUnknownYieldsEmpty 验证未取得用量时给出空集合而不是 nil，
// 调用方序列化后得到 {}，仍是一行合法的用量事实。
func TestUsageFromDomainUnknownYieldsEmpty(t *testing.T) {
	got, dropped, _ := UsageFromDomain(domain.Usage{})
	if got == nil {
		t.Fatal("空用量应返回空 map 而不是 nil")
	}
	if len(got) != 0 {
		t.Errorf("空用量不应产生分量，得到 %#v", got)
	}
	if len(dropped) != 0 {
		t.Errorf("空用量不应有被丢弃的分量，得到 %v", dropped)
	}
}

// TestUsageFromDomainPrefersTieredCacheWrite 验证分档口径优先：
// 分档非零时只落两档分量，不分档分量不落。
func TestUsageFromDomainPrefersTieredCacheWrite(t *testing.T) {
	usage := domain.Usage{
		Source:             domain.UsageSourceUpstream,
		InputTokens:        100,
		CacheWrite5mTokens: 6,
		CacheWrite1hTokens: 4,
	}
	got, dropped, conflicts := UsageFromDomain(usage)
	want := map[Metric]int{
		MetricInputToken:   100,
		MetricCacheWrite5m: 6,
		MetricCacheWrite1h: 4,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分量 = %#v，期望 %#v", got, want)
	}
	if len(dropped) != 0 {
		t.Errorf("不应有被丢弃的分量，得到 %v", dropped)
	}
	if len(conflicts) != 0 {
		t.Errorf("不应有口径冲突，得到 %v", conflicts)
	}
	if _, ok := got[MetricCacheWriteToken]; ok {
		t.Error("分档非零时不应落不分档分量")
	}
}

// TestUsageFromDomainTieredSingleBand 验证只有一档非零时也按分档口径处理。
func TestUsageFromDomainTieredSingleBand(t *testing.T) {
	usage := domain.Usage{
		Source:             domain.UsageSourceUpstream,
		InputTokens:        100,
		CacheWrite1hTokens: 4,
	}
	got, _, conflicts := UsageFromDomain(usage)
	want := map[Metric]int{
		MetricInputToken:   100,
		MetricCacheWrite1h: 4,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分量 = %#v，期望 %#v", got, want)
	}
	if len(conflicts) != 0 {
		t.Errorf("不应有口径冲突，得到 %v", conflicts)
	}
}

// TestUsageFromDomainFallsBackToFlatCacheWrite 验证分档全零而不分档非零时落 cache_write_token。
func TestUsageFromDomainFallsBackToFlatCacheWrite(t *testing.T) {
	usage := domain.Usage{
		Source:           domain.UsageSourceUpstream,
		InputTokens:      100,
		CacheWriteTokens: 7,
	}
	got, _, conflicts := UsageFromDomain(usage)
	want := map[Metric]int{
		MetricInputToken:      100,
		MetricCacheWriteToken: 7,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分量 = %#v，期望 %#v", got, want)
	}
	if len(conflicts) != 0 {
		t.Errorf("不应有口径冲突，得到 %v", conflicts)
	}
}

// TestUsageFromDomainReportsCacheWriteConflict 验证两套口径同时非零时以分档为准并报告冲突。
func TestUsageFromDomainReportsCacheWriteConflict(t *testing.T) {
	usage := domain.Usage{
		Source:             domain.UsageSourceUpstream,
		InputTokens:        100,
		CacheWriteTokens:   7,
		CacheWrite5mTokens: 6,
	}
	got, _, conflicts := UsageFromDomain(usage)
	want := map[Metric]int{
		MetricInputToken:   100,
		MetricCacheWrite5m: 6,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分量 = %#v，期望 %#v", got, want)
	}
	if len(conflicts) != 1 {
		t.Fatalf("应报告一条口径冲突，得到 %v", conflicts)
	}
	if _, ok := got[MetricCacheWriteToken]; ok {
		t.Error("冲突时不应落不分档分量")
	}
}
