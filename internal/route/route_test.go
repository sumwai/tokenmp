package route

import (
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// seqIntN 返回一个按给定序列产出随机下标、再循环回开头的注入随机源。
//
// 用它把「选中了哪一条」固定下来，从而对加权随机做确定性断言，
// 而不是统计一堆重复试验的频次。
func seqIntN(values ...int) func(int) int {
	index := 0
	return func(int) int {
		value := values[index%len(values)]
		index++
		return value
	}
}

func TestPickWeightedIndex(t *testing.T) {
	tests := []struct {
		name    string
		weights []int
		roll    int
		want    int
	}{
		{name: "权重大的命中区间更宽", weights: []int{1, 3}, roll: 3, want: 1},
		{name: "首个区间落在首位", weights: []int{1, 3}, roll: 0, want: 0},
		{name: "非正权重视为 1", weights: []int{-5, 2}, roll: 0, want: 0},
		{name: "非正权重之后按 1 累计", weights: []int{-5, 2}, roll: 1, want: 1},
		{name: "全部权重为 0 时均匀随机", weights: []int{0, 0, 0}, roll: 2, want: 2},
		{name: "单条权重", weights: []int{0}, roll: 0, want: 0},
		{name: "空权重表", weights: nil, roll: 0, want: 0},
		{name: "越界取值被钳制", weights: []int{1, 1}, roll: 99, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickWeightedIndex(tt.weights, func(int) int { return tt.roll })
			if got != tt.want {
				t.Errorf("选中下标 = %d，期望 %d", got, tt.want)
			}
		})
	}
}

func TestSanitizeRequestOverrides(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "合法对象原样保留", raw: `{"temperature":0.2}`, want: `{"temperature":0.2}`},
		{name: "空值视为未配置", raw: "", want: ""},
		{name: "JSON null 视为未配置", raw: "null", want: ""},
		{name: "数组丢弃", raw: `[1,2]`, want: ""},
		{name: "非 JSON 文本丢弃", raw: "not-json", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeRequestOverrides(store.RouteCandidate{ChannelID: 7, RequestOverrides: []byte(tt.raw)})
			if string(got) != tt.want {
				t.Errorf("结果 = %q，期望 %q", got, tt.want)
			}
		})
	}
}

func TestOrderCandidatesByWeight(t *testing.T) {
	tests := []struct {
		name       string
		candidates []store.RouteCandidate
		rolls      []int
		want       []uint64
	}{
		{
			name: "组内选中项置首且其余保持顺序",
			candidates: []store.RouteCandidate{
				{ChannelID: 11, Priority: 200, Weight: 0},
				{ChannelID: 12, Priority: 200, Weight: 5},
				{ChannelID: 13, Priority: 200, Weight: 0},
				{ChannelID: 21, Priority: 100, Weight: 3},
				{ChannelID: 22, Priority: 100, Weight: 1},
			},
			// 高优先级组权重 [1,5,1]，roll=2 落在第二条；低优先级组 roll=0 不动。
			rolls: []int{2, 0},
			want:  []uint64{12, 11, 13, 21, 22},
		},
		{
			name: "全部权重为 0 时按均匀随机选中",
			candidates: []store.RouteCandidate{
				{ChannelID: 1, Priority: 100, Weight: 0},
				{ChannelID: 2, Priority: 100, Weight: 0},
				{ChannelID: 3, Priority: 100, Weight: 0},
			},
			rolls: []int{2},
			want:  []uint64{3, 1, 2},
		},
		{
			name: "单条候选的组原样保留",
			candidates: []store.RouteCandidate{
				{ChannelID: 1, Priority: 200, Weight: 9},
				{ChannelID: 2, Priority: 100, Weight: 1},
			},
			rolls: []int{0},
			want:  []uint64{1, 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orderCandidatesByWeight(tt.candidates, seqIntN(tt.rolls...))
			got := make([]uint64, 0, len(tt.candidates))
			for _, c := range tt.candidates {
				got = append(got, c.ChannelID)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("候选数 = %d，期望 %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("顺序 = %v，期望 %v", got, tt.want)
				}
			}
		})
	}
}

// chainIDs 把回退链压成渠道 id 列表，供顺序整体比对。
func chainIDs(routes []domain.Route) []uint64 {
	ids := make([]uint64, 0, len(routes))
	for _, route := range routes {
		ids = append(ids, route.ChannelID)
	}
	return ids
}

// assertChainIDs 断言回退链的渠道 id 顺序与期望一致。
func assertChainIDs(t *testing.T, routes []domain.Route, want []uint64) {
	t.Helper()
	got := chainIDs(routes)
	if len(got) != len(want) {
		t.Fatalf("链长度 = %d，期望 %d（实际 %v）", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("链顺序 = %v，期望 %v", got, want)
		}
	}
}

// TestRouteChainSameProtocolSegmentComesFirst 守护分层顺序：同协议候选整段在前，
// 即使跨协议候选的 priority 更高也不得插到前面。
func TestRouteChainSameProtocolSegmentComesFirst(t *testing.T) {
	same := []store.RouteCandidate{
		{ChannelID: 11, Priority: 10, Weight: 1, ChannelType: store.ChannelTypeOpenAIChat, BaseURL: "https://same.example.com"},
		{ChannelID: 12, Priority: 5, Weight: 1, ChannelType: store.ChannelTypeOpenAIChat, BaseURL: "https://same.example.com"},
	}
	cross := []store.RouteCandidate{
		{ChannelID: 21, Priority: 999, Weight: 1, ChannelType: store.ChannelTypeAnthropicMessages, BaseURL: "https://cross.example.com"},
	}

	routes := RouteChain(domain.ProtocolOpenAIChat, same, cross, seqIntN(0, 0))
	assertChainIDs(t, routes, []uint64{11, 12, 21})
	if routes[0].Protocol != domain.ProtocolOpenAIChat || routes[1].Protocol != domain.ProtocolOpenAIChat {
		t.Errorf("同协议段的协议应为客户端协议，得到 %q、%q", string(routes[0].Protocol), string(routes[1].Protocol))
	}
	if routes[2].Protocol != domain.ProtocolAnthropicMessages {
		t.Errorf("跨协议候选的协议应取渠道方言，得到 %q", string(routes[2].Protocol))
	}
	// 端点段跟着上游协议走：跨协议候选的地址不能拼成客户端方言的端点。
	if routes[2].BaseURL != "https://cross.example.com/messages" {
		t.Errorf("跨协议候选地址 = %q，期望按 anthropic 端点段拼接", routes[2].BaseURL)
	}
}

// TestRouteChainDropsCrossProtocolDuplicates 守护去重：不限协议查询必然包含同协议行，
// 同一条渠道不得在同协议段失败后又在跨协议段重试一次。
func TestRouteChainDropsCrossProtocolDuplicates(t *testing.T) {
	same := []store.RouteCandidate{
		{ChannelID: 11, ChannelType: store.ChannelTypeOpenAIChat},
	}
	// 跨协议段同时含同一条同协议渠道与一条真正的跨协议渠道。
	cross := []store.RouteCandidate{
		{ChannelID: 11, ChannelType: store.ChannelTypeOpenAIChat},
		{ChannelID: 21, ChannelType: store.ChannelTypeAnthropicMessages},
	}

	routes := RouteChain(domain.ProtocolOpenAIChat, same, cross, seqIntN(0, 0))
	assertChainIDs(t, routes, []uint64{11, 21})
}

// TestRouteChainSkipsUnknownChannelType 守护读方向宽容：渠道方言不在可重建集合里时
// 跳过该候选，而不是让它进链后由流水线报「没有适配器」。
func TestRouteChainSkipsUnknownChannelType(t *testing.T) {
	cross := []store.RouteCandidate{
		{ChannelID: 31, ChannelType: store.ChannelType("openai_embeddings")},
		{ChannelID: 32},
		{ChannelID: 33, ChannelType: store.ChannelTypeOpenAIResponses},
	}

	routes := RouteChain(domain.ProtocolOpenAIChat, nil, cross, seqIntN(0, 0))
	assertChainIDs(t, routes, []uint64{33})
}

// TestRouteChainOrderCandidatesWithinSegments 守护段内行为与同协议路径一致：
// 同优先级组内按加权随机定首选，组与组之间按 priority 降序。
func TestRouteChainOrderCandidatesWithinSegments(t *testing.T) {
	same := []store.RouteCandidate{
		{ChannelID: 11, Priority: 100, Weight: 0, ChannelType: store.ChannelTypeOpenAIChat},
		{ChannelID: 12, Priority: 100, Weight: 5, ChannelType: store.ChannelTypeOpenAIChat},
	}
	cross := []store.RouteCandidate{
		{ChannelID: 21, Priority: 100, Weight: 0, ChannelType: store.ChannelTypeAnthropicMessages},
		{ChannelID: 22, Priority: 100, Weight: 7, ChannelType: store.ChannelTypeAnthropicMessages},
	}

	// 两个段各消耗一次随机取值：同协议段 [1,5] 取 roll=1 命中 12，跨协议段 [1,7] 取 roll=0 命中 21。
	routes := RouteChain(domain.ProtocolOpenAIChat, same, cross, seqIntN(1, 0))
	assertChainIDs(t, routes, []uint64{12, 11, 21, 22})
}

// TestRouteChainEmpty 守护两级都无候选时返回空链，由流水线统一回「没有可用渠道」。
func TestRouteChainEmpty(t *testing.T) {
	if routes := RouteChain(domain.ProtocolOpenAIChat, nil, nil, seqIntN(0)); len(routes) != 0 {
		t.Fatalf("无候选时应返回空链，实际 %#v", routes)
	}
}
