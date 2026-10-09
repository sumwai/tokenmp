package gateway

import (
	"context"
	"database/sql"

	"github.com/sumwai/tokenmp/internal/store"
)

// 商家域（/api/v1/partner/*）在装配层的替身实现。
//
// 装配用例不覆盖商家域（它的行为在 internal/partner 里单测），这里只让
// fakeGatewayStore 满足 gatewayStore：未绑定的商家与空集合是这套替身的自然零值。

func (f *fakeGatewayStore) MerchantByOwner(context.Context, uint64) (*store.Merchant, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) ChannelsByMerchant(context.Context, uint64, *bool, int, int) ([]store.Channel, int, error) {
	return nil, 0, nil
}

func (f *fakeGatewayStore) CredentialsByMerchant(context.Context, uint64, int, int) ([]store.CredentialRow, int, error) {
	return nil, 0, nil
}

func (f *fakeGatewayStore) InsertChannel(context.Context, store.Channel) (uint64, error) {
	return 0, nil
}

func (f *fakeGatewayStore) InsertCredential(context.Context, store.CredentialRow) (uint64, error) {
	return 0, nil
}

func (f *fakeGatewayStore) SetChannelEnabledForMerchant(context.Context, uint64, uint64, bool) (bool, error) {
	return false, nil
}

func (f *fakeGatewayStore) SetCredentialEnabledForMerchant(context.Context, uint64, uint64, bool) (bool, error) {
	return false, nil
}

func (f *fakeGatewayStore) MerchantUsageStats(context.Context, store.MerchantUsageStatsQuery) ([]store.UsageStatsItem, error) {
	return nil, nil
}
