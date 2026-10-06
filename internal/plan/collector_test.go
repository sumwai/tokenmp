package plan

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// fakeRepo 是采集侧数据面的记录型替身。
type fakeRepo struct {
	targets []ProbeTarget
	plans   map[string]*UpstreamPlan

	mu    sync.Mutex
	saved []savedResult
	// savedCh 在每次保存时投递一条，供 Run 用例等待首轮采集。
	savedCh chan struct{}

	targetsErr error
	plansErr   error
	saveErr    error
}

type savedResult struct {
	planID    uint64
	snapshot  []byte
	checkedAt time.Time
	used      []QuotaUsed
}

func (f *fakeRepo) ProbeTargets(context.Context) ([]ProbeTarget, error) {
	if f.targetsErr != nil {
		return nil, f.targetsErr
	}
	return f.targets, nil
}

func (f *fakeRepo) PlansByCredGroup(_ context.Context, merchantID uint64, credGroup string) (*UpstreamPlan, error) {
	if f.plansErr != nil {
		return nil, f.plansErr
	}
	p, ok := f.plans[fmt.Sprintf("%d/%s", merchantID, credGroup)]
	if !ok {
		return nil, errors.New("plan: 套餐不存在")
	}
	return p, nil
}

func (f *fakeRepo) SaveProbeResult(_ context.Context, planID uint64, snapshot []byte, checkedAt time.Time, used []QuotaUsed) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, savedResult{planID: planID, snapshot: snapshot, checkedAt: checkedAt, used: used})
	if f.savedCh != nil {
		select {
		case f.savedCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeRepo) savedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved)
}

// probeConfigJSON 生成一段渠道 config，探针指向给定地址并声明一条 request/day 限额。
func probeConfigJSON(url string) []byte {
	return []byte(fmt.Sprintf(
		`{"probe":{"url":%q,"headers":{"X-Probe":"yes"},"metrics":[{"metric":"request","period":"day","used_path":"$.used"}]}}`,
		url))
}

// newProbeServer 起一个按 body 回应的探针服务端。
func newProbeServer(t *testing.T, status int, body string, hits *int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		if got := r.Header.Get("X-Probe"); got != "yes" {
			http.Error(w, "缺少探针头", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// testPlan 构造一条含单条 request/day 限额的套餐。
func testPlan() *UpstreamPlan {
	return &UpstreamPlan{
		ID:         7,
		MerchantID: 3,
		CredGroup:  "group-a",
		Name:       "测试套餐",
		Quotas: []Quota{{
			ID:          11,
			PlanID:      7,
			Metric:      billing.MetricRequest,
			WindowKind:  billing.WindowKindCalendar,
			Period:      billing.PeriodDay,
			LimitAmount: decimal.RequireFromString("100"),
		}},
	}
}

func TestCollectorCollectOnceSavesSnapshot(t *testing.T) {
	hits := 0
	server := newProbeServer(t, http.StatusOK, `{"used":42}`, &hits)
	repo := &fakeRepo{
		targets: []ProbeTarget{{ChannelID: 1, MerchantID: 3, CredGroup: "group-a", Config: probeConfigJSON(server.URL)}},
		plans:   map[string]*UpstreamPlan{"3/group-a": testPlan()},
	}
	fixed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	collector, err := NewCollector(repo, CollectorOptions{
		Interval: time.Minute,
		Clock:    func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("构造采集器失败：%v", err)
	}
	collector.CollectOnce(context.Background())

	if hits != 1 {
		t.Fatalf("探针调用次数 = %d，期望 1", hits)
	}
	if repo.savedCount() != 1 {
		t.Fatalf("保存次数 = %d，期望 1", repo.savedCount())
	}
	saved := repo.saved[0]
	if saved.planID != 7 || string(saved.snapshot) != `{"used":42}` {
		t.Errorf("保存内容不符：%+v", saved)
	}
	if !saved.checkedAt.Equal(fixed) {
		t.Errorf("采集时刻 = %s，期望 %s", saved.checkedAt, fixed)
	}
	if len(saved.used) != 1 || saved.used[0].QuotaID != 11 ||
		!saved.used[0].Used.Equal(decimal.RequireFromString("42")) {
		t.Errorf("限额已用量不符：%+v", saved.used)
	}
}

func TestCollectorDedupesByCredGroup(t *testing.T) {
	hits := 0
	server := newProbeServer(t, http.StatusOK, `{"used":1}`, &hits)
	config := probeConfigJSON(server.URL)
	repo := &fakeRepo{
		targets: []ProbeTarget{
			{ChannelID: 1, MerchantID: 3, CredGroup: "group-a", Config: config},
			{ChannelID: 2, MerchantID: 3, CredGroup: "group-a", Config: config},
			{ChannelID: 3, MerchantID: 3, CredGroup: "group-b", Config: config},
		},
		plans: map[string]*UpstreamPlan{
			"3/group-a": testPlan(),
			"3/group-b": testPlan(),
		},
	}
	collector, err := NewCollector(repo, CollectorOptions{Interval: time.Minute})
	if err != nil {
		t.Fatalf("构造采集器失败：%v", err)
	}
	collector.CollectOnce(context.Background())

	if hits != 2 {
		t.Errorf("探针调用次数 = %d，期望 2（同 cred_group 去重）", hits)
	}
	if repo.savedCount() != 2 {
		t.Errorf("保存次数 = %d，期望 2", repo.savedCount())
	}
}

func TestCollectorKeepsOldSnapshotOnFailure(t *testing.T) {
	tests := []struct {
		name string
		repo *fakeRepo
		body string
	}{
		{
			name: "探针不可达",
			repo: &fakeRepo{
				targets: []ProbeTarget{{ChannelID: 1, MerchantID: 3, CredGroup: "group-a", Config: probeConfigJSON("http://127.0.0.1:1")}},
				plans:   map[string]*UpstreamPlan{"3/group-a": testPlan()},
			},
		},
		{
			name: "探针取值与套餐不匹配",
			repo: &fakeRepo{
				targets: []ProbeTarget{{ChannelID: 1, MerchantID: 3, CredGroup: "group-a"}},
				plans: map[string]*UpstreamPlan{"3/group-a": func() *UpstreamPlan {
					p := testPlan()
					p.Quotas[0].Metric = billing.MetricInputToken
					return p
				}()},
			},
		},
		{
			name: "没有对应套餐",
			repo: &fakeRepo{
				targets: []ProbeTarget{{ChannelID: 1, MerchantID: 3, CredGroup: "group-a"}},
				plans:   map[string]*UpstreamPlan{},
			},
		},
		{
			name: "保存失败",
			repo: &fakeRepo{
				targets: []ProbeTarget{{ChannelID: 1, MerchantID: 3, CredGroup: "group-a"}},
				plans:   map[string]*UpstreamPlan{"3/group-a": testPlan()},
				saveErr: errors.New("写库失败"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newProbeServer(t, http.StatusOK, `{"used":1}`, nil)
			for i := range tt.repo.targets {
				if len(tt.repo.targets[i].Config) == 0 {
					tt.repo.targets[i].Config = probeConfigJSON(server.URL)
				}
			}
			collector, err := NewCollector(tt.repo, CollectorOptions{Interval: time.Minute})
			if err != nil {
				t.Fatalf("构造采集器失败：%v", err)
			}
			collector.CollectOnce(context.Background())
			if tt.repo.savedCount() != 0 {
				t.Errorf("失败时不应保存，实际保存 %d 次", tt.repo.savedCount())
			}
		})
	}
}

func TestCollectorSkipsChannelsWithoutProbe(t *testing.T) {
	repo := &fakeRepo{
		targets: []ProbeTarget{{ChannelID: 1, MerchantID: 3, CredGroup: "group-a", Config: []byte(`{"signin_header":{"name":"x"}}`)}},
		plans:   map[string]*UpstreamPlan{"3/group-a": testPlan()},
	}
	collector, err := NewCollector(repo, CollectorOptions{Interval: time.Minute})
	if err != nil {
		t.Fatalf("构造采集器失败：%v", err)
	}
	collector.CollectOnce(context.Background())
	if repo.savedCount() != 0 {
		t.Errorf("未声明探针的渠道不应采集，实际保存 %d 次", repo.savedCount())
	}
}

func TestCollectorRunCollectsImmediately(t *testing.T) {
	server := newProbeServer(t, http.StatusOK, `{"used":1}`, nil)
	repo := &fakeRepo{
		targets: []ProbeTarget{{ChannelID: 1, MerchantID: 3, CredGroup: "group-a", Config: probeConfigJSON(server.URL)}},
		plans:   map[string]*UpstreamPlan{"3/group-a": testPlan()},
		savedCh: make(chan struct{}, 1),
	}
	collector, err := NewCollector(repo, CollectorOptions{Interval: time.Hour})
	if err != nil {
		t.Fatalf("构造采集器失败：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		collector.Run(ctx)
		close(done)
	}()
	select {
	case <-repo.savedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("等待首轮采集超时")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Run 未退出")
	}
}

func TestNewCollectorRejectsBadOptions(t *testing.T) {
	if _, err := NewCollector(nil, CollectorOptions{Interval: time.Minute}); err == nil {
		t.Error("缺少数据面应当报错")
	}
	if _, err := NewCollector(&fakeRepo{}, CollectorOptions{Interval: 0}); err == nil {
		t.Error("零间隔应当报错")
	}
}
