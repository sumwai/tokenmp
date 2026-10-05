package admin

import (
	"context"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是用量流水与人工调账的管理动作。

// ListUsage 列出用量流水；accountID 为 0 时列出全部，since 为零值时不做时间过滤。
func (s *Service) ListUsage(ctx context.Context, accountID uint64, since time.Time) ([]store.UsageListRow, error) {
	return s.store.ListUsage(ctx, accountID, since)
}

// AdjustmentInput 是人工调账的输入。
type AdjustmentInput struct {
	AccountID uint64
	Amount    string
	Reason    string
	Operator  string
}

// AddAdjustment 追加一条调账流水。
//
// amount 允许为负（退费）：正负号是调账方向本身，不在这里收窄。
// operator 必填，审计链要能回答「谁发起的」。
func (s *Service) AddAdjustment(ctx context.Context, in AdjustmentInput) (uint64, error) {
	if err := requireID("调账 account", in.AccountID); err != nil {
		return 0, err
	}
	if err := requireDecimal("调账 amount", in.Amount); err != nil {
		return 0, err
	}
	if err := requireString("调账 reason", in.Reason); err != nil {
		return 0, err
	}
	if err := requireString("调账 operator", in.Operator); err != nil {
		return 0, err
	}
	return s.store.InsertAdjustment(ctx, store.Adjustment{
		AccountID:   in.AccountID,
		DeltaAmount: in.Amount,
		Reason:      in.Reason,
		Operator:    in.Operator,
		CreatedAt:   s.now(),
	})
}

// ListAdjustments 列出调账记录；accountID 为 0 时列出全部。
func (s *Service) ListAdjustments(ctx context.Context, accountID uint64) ([]store.Adjustment, error) {
	return s.store.ListAdjustments(ctx, accountID)
}
