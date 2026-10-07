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

func (f *fakeGatewayStore) WebInsertUser(context.Context, store.WebUser) (uint64, error) {
	return 1, nil
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
