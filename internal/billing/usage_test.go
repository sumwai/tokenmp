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
	got, dropped := UsageFromDomain(usage)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分量 = %#v，期望 %#v", got, want)
	}
	if len(dropped) != 0 {
		t.Errorf("不应有被丢弃的分量，得到 %v", dropped)
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
	got, dropped := UsageFromDomain(usage)
	want := map[Metric]int{MetricInputToken: 5, MetricOutputToken: 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("分量 = %#v，期望 %#v", got, want)
	}
	if len(dropped) != 0 {
		t.Errorf("不应有被丢弃的分量，得到 %v", dropped)
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
	got, dropped := UsageFromDomain(usage)
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
	got, dropped := UsageFromDomain(domain.Usage{})
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
