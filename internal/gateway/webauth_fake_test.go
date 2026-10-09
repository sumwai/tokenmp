package gateway

import (
	"context"
	"database/sql"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件给装配测试的存储替身补齐页面认证的数据面。
//
// 装配测试不触达 /api/v1/auth/* 的业务路径，方法一律返回空结果：
// 真实行为由 internal/auth 的单测覆盖，替身只需满足接口让编译期断言成立。

func (f *fakeGatewayStore) WebUserByLogin(context.Context, string) (*store.WebUser, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) WebUserByEmail(context.Context, string) (*store.WebUser, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) WebInsertUserWithAccount(context.Context, store.WebUser, store.Account) (uint64, uint64, error) {
	return 1, 1, nil
}

func (f *fakeGatewayStore) WebInsertSession(context.Context, store.WebSession) (uint64, error) {
	return 1, nil
}

func (f *fakeGatewayStore) WebSessionByAccess(context.Context, string) (*store.WebSessionWithUser, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) WebSessionByRefresh(context.Context, string) (*store.WebSessionWithUser, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) WebSessionByPrevRefresh(context.Context, string) (*store.WebSessionWithUser, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) WebRotateSession(context.Context, uint64, string, string, string,
	time.Time, time.Time) (bool, error) {
	return true, nil
}

func (f *fakeGatewayStore) WebRevokeSession(context.Context, uint64) error { return nil }

func (f *fakeGatewayStore) WebRevokeUserSessions(context.Context, uint64) error { return nil }

func (f *fakeGatewayStore) WebIdentityProviders(context.Context, uint64) ([]string, error) {
	return []string{}, nil
}

func (f *fakeGatewayStore) WebRevokeOtherSessions(context.Context, uint64, uint64) error { return nil }

func (f *fakeGatewayStore) WebInsertOTP(context.Context, string, string, string, time.Time) error {
	return nil
}

func (f *fakeGatewayStore) WebConsumeOTP(context.Context, string, string, string, time.Time) (bool, error) {
	return false, nil
}

func (f *fakeGatewayStore) WebUpdatePassword(context.Context, uint64, string) error { return nil }

func (f *fakeGatewayStore) WebEraseUser(context.Context, uint64) error { return nil }

func (f *fakeGatewayStore) WebUserByID(context.Context, uint64) (*store.WebUser, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) WebIdentityByProvider(context.Context, string, string) (*store.WebIdentity, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) WebInsertIdentity(context.Context, store.WebIdentity) error { return nil }

// 以下四个方法补齐用户级端点的密钥自助管理依赖：装配测试不触达密钥业务路径，
// 一律返回空结果，真实行为由 internal/user 的单测覆盖。

func (f *fakeGatewayStore) InsertAPIKey(context.Context, store.APIKey) (uint64, error) {
	return 1, nil
}

func (f *fakeGatewayStore) ListAPIKeysByAccount(context.Context, uint64, *bool, int, int) ([]store.APIKey, int, error) {
	return nil, 0, nil
}

func (f *fakeGatewayStore) APIKeyByID(context.Context, uint64) (*store.APIKey, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) SetAPIKeyEnabled(context.Context, uint64, bool) error { return nil }

// 以下两个方法补齐用户级端点的用量流水与模型目录依赖，同样返回空结果：
// 装配测试只验证路径挂载与会话鉴权，业务口径由 internal/user 的单测覆盖。

func (f *fakeGatewayStore) ListAccountUsage(context.Context, store.AccountUsageFilter) ([]store.AccountUsageRow, int, error) {
	return nil, 0, nil
}

func (f *fakeGatewayStore) AccountUsageStats(context.Context, store.UsageStatsQuery) ([]store.UsageStatsItem, error) {
	return nil, nil
}

func (f *fakeGatewayStore) ListAccountModels(context.Context, uint64) ([]store.AccountModel, error) {
	return nil, nil
}

// 以下四个方法补齐用户级端点的请求记录依赖，同样返回空结果。

func (f *fakeGatewayStore) ListRequestLogs(context.Context, store.RequestLogFilter) ([]store.RequestLogRow, int, error) {
	return nil, 0, nil
}

func (f *fakeGatewayStore) RequestLogByRequestID(context.Context, uint64, string) (*store.RequestLogRow, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeGatewayStore) RequestAttempts(context.Context, string) ([]store.RequestAttempt, error) {
	return nil, nil
}

func (f *fakeGatewayStore) RequestStats(context.Context, store.RequestStatsQuery) ([]store.RequestStatsItem, error) {
	return nil, nil
}
