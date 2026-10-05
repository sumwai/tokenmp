package main

import (
	"testing"

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
