package partner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖商家域对账单端点：作用域来自会话推导出的商家、账期参数的成对与有序、
// 出账失败与缺装配的处置，以及对外形状（回显金额但绝不回显商家标识）。全链不经数据库。

// fakeSettlements 是出账替身：记录收到的商家与账期，返回注入的对账单。
type fakeSettlements struct {
	bill settlement.BillView
	err  error

	merchantID uint64
	from, to   time.Time
	calls      int
}

func (f *fakeSettlements) SettlementBill(_ context.Context, merchantID uint64, from, to time.Time) (settlement.BillView, error) {
	f.calls++
	f.merchantID, f.from, f.to = merchantID, from, to
	if f.err != nil {
		return settlement.BillView{}, f.err
	}
	return f.bill, nil
}

// sampleBill 是一张对账单夹具：金额与抽成率都是出账侧给的十进制字符串。
func sampleBill() settlement.BillView {
	return settlement.BillView{
		MerchantID:     2,
		Period:         "month",
		From:           "2026-09-01T00:00:00Z",
		To:             "2026-10-01T00:00:00Z",
		CommissionRate: "0.1000",
		Trades:         3,
		GrossSales:     "100.00000000",
		Commission:     "10.00000000",
		UpstreamCost:   "30.00000000",
		Payout:         "60.00000000",
	}
}

func TestSettlementScopedToSessionMerchant(t *testing.T) {
	sessions, st := partnerSubject()
	bills := &fakeSettlements{bill: sampleBill()}
	h := NewHandler(Options{Sessions: sessions, Store: st, Settlements: bills})

	code, env := do(t, h, http.MethodGet, SettlementPath, "", testToken)
	if code != http.StatusOK {
		t.Fatalf("对账单应回 200，得到 %d", code)
	}
	if bills.calls != 1 || bills.merchantID != 2 {
		t.Fatalf("出账作用域 = %d（调用 %d 次），期望会话推导出的商家 2", bills.merchantID, bills.calls)
	}
	// 不给区间时把零值交给出账侧，由它按商家账期取上一个完整自然周期。
	if !bills.from.IsZero() || !bills.to.IsZero() {
		t.Errorf("缺省账期应透传零值，得到 %s ~ %s", bills.from, bills.to)
	}

	var data map[string]json.RawMessage
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("data 不是对象：%v", err)
	}
	if _, ok := data["merchant_id"]; ok {
		t.Error("对账单不应回显 merchant_id：它就是会话本身")
	}
	for _, field := range []string{
		"period", "from", "to", "commission_rate", "trades",
		"gross_sales", "commission", "upstream_cost", "payout",
	} {
		if _, ok := data[field]; !ok {
			t.Errorf("对账单缺少字段 %q", field)
		}
	}
	var amounts struct {
		Period         string `json:"period"`
		Trades         int64  `json:"trades"`
		CommissionRate string `json:"commission_rate"`
		GrossSales     string `json:"gross_sales"`
		Commission     string `json:"commission"`
		UpstreamCost   string `json:"upstream_cost"`
		Payout         string `json:"payout"`
	}
	// 解析进 string 字段本身就是断言：金额若写成 JSON 数字，浏览器侧会按双精度浮点丢精度。
	if err := json.Unmarshal(env["data"], &amounts); err != nil {
		t.Fatalf("金额字段不是字符串：%v", err)
	}
	if amounts.Period != "month" || amounts.Trades != 3 || amounts.CommissionRate != "0.1000" {
		t.Errorf("对账单字段不符：%+v", amounts)
	}
	if amounts.GrossSales != "100.00000000" || amounts.Payout != "60.00000000" {
		t.Errorf("金额原样透传失败：%+v", amounts)
	}
}

func TestSettlementWindowParams(t *testing.T) {
	const from = "2026-08-01T00:00:00Z"
	const to = "2026-09-01T00:00:00Z"
	sessions, st := partnerSubject()
	bills := &fakeSettlements{bill: sampleBill()}
	h := NewHandler(Options{Sessions: sessions, Store: st, Settlements: bills})

	code, _ := do(t, h, http.MethodGet, SettlementPath+"?from="+from+"&to="+to, "", testToken)
	if code != http.StatusOK {
		t.Fatalf("带账期应回 200，得到 %d", code)
	}
	wantFrom, err := time.Parse(time.RFC3339, from)
	if err != nil {
		t.Fatalf("解析夹具时刻：%v", err)
	}
	wantTo, err := time.Parse(time.RFC3339, to)
	if err != nil {
		t.Fatalf("解析夹具时刻：%v", err)
	}
	if !bills.from.Equal(wantFrom) || !bills.to.Equal(wantTo) {
		t.Errorf("账期 = %s ~ %s，期望 %s ~ %s", bills.from, bills.to, from, to)
	}

	bad := []struct {
		name  string
		query string
	}{
		{name: "只给起点", query: "?from=" + from},
		{name: "只给终点", query: "?to=" + to},
		{name: "终点不晚于起点", query: "?from=" + to + "&to=" + from},
		{name: "时刻不可解析", query: "?from=昨天&to=" + to},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := do(t, h, http.MethodGet, SettlementPath+tc.query, "", testToken); code != http.StatusBadRequest {
				t.Errorf("%s 应回 400，得到 %d", tc.query, code)
			}
		})
	}
}

// TestSettlementRequiresPartnerRole 断言非商家身份回 403（会话有效，缺的是身份）。
func TestSettlementRequiresPartnerRole(t *testing.T) {
	sessions, st := partnerSubject()
	sessions.roles = []string{store.RoleMember}
	h := NewHandler(Options{Sessions: sessions, Store: st, Settlements: &fakeSettlements{}})
	if code, _ := do(t, h, http.MethodGet, SettlementPath, "", testToken); code != http.StatusForbidden {
		t.Fatalf("无商家身份应回 403，得到 %d", code)
	}
}

// TestSettlementWithoutMerchant 断言有商家身份但没有名下商家时回 403（尚未开通）。
func TestSettlementWithoutMerchant(t *testing.T) {
	sessions, st := partnerSubject()
	st.merchant = nil
	h := NewHandler(Options{Sessions: sessions, Store: st, Settlements: &fakeSettlements{}})
	if code, _ := do(t, h, http.MethodGet, SettlementPath, "", testToken); code != http.StatusForbidden {
		t.Fatalf("商家未开通应回 403，得到 %d", code)
	}
}

func TestSettlementRejectsNonGet(t *testing.T) {
	sessions, st := partnerSubject()
	h := NewHandler(Options{Sessions: sessions, Store: st, Settlements: &fakeSettlements{}})
	if code, _ := do(t, h, http.MethodPost, SettlementPath, "", testToken); code != http.StatusBadRequest {
		t.Fatalf("非 GET 应回 400，得到 %d", code)
	}
}

// TestSettlementWithoutImplementation 断言装配缺少出账实现时回 500。
//
// 不能回「没有对账单」：那是把部署问题说成业务事实，页面会当成正常的空账期。
func TestSettlementWithoutImplementation(t *testing.T) {
	sessions, st := partnerSubject()
	h := NewHandler(Options{Sessions: sessions, Store: st})
	if code, _ := do(t, h, http.MethodGet, SettlementPath, "", testToken); code != http.StatusInternalServerError {
		t.Fatalf("缺出账实现应回 500，得到 %d", code)
	}
}

// TestSettlementErrorIsInternal 断言出账失败回 500，且不把底层错误文案带出去。
func TestSettlementErrorIsInternal(t *testing.T) {
	sessions, st := partnerSubject()
	bills := &fakeSettlements{err: errors.New("store: 聚合 billing_usage 失败: table doesn't exist")}
	h := NewHandler(Options{Sessions: sessions, Store: st, Settlements: bills})
	code, env := do(t, h, http.MethodGet, SettlementPath, "", testToken)
	if code != http.StatusInternalServerError {
		t.Fatalf("出账失败应回 500，得到 %d", code)
	}
	var message string
	if err := json.Unmarshal(env["message"], &message); err != nil {
		t.Fatalf("message 不是字符串：%v", err)
	}
	for _, fragment := range []string{"table", "billing_usage", "store:"} {
		if strings.Contains(message, fragment) {
			t.Errorf("错误文案泄露了底层细节 %q：%q", fragment, message)
		}
	}
}
