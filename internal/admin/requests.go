package admin

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是请求记录的管理面读路径。
//
// 与管理面其余清单不同，这些读法不带账户作用域：排障要定位任意账户的请求。作用域
// 收敛在入口（HTTPS 管理面要求 ops 能力，CLI 是本地运维工具），因此数据面允许
// accountID 为 0 表示不限账户。

// requestStatuses 是请求终态的取值集合，与存储层和契约一致。
var requestStatuses = map[string]struct{}{
	"success":   {},
	"failed":    {},
	"cancelled": {},
}

// requestStatsGroups 是聚合分组维度的取值集合。
var requestStatsGroups = map[string]struct{}{
	"day":     {},
	"model":   {},
	"status":  {},
	"account": {},
}

// RequestLogReader 是请求记录读路径依赖的数据面。
//
// 与 Store 分开声明：只读清单的测试替身不必实现整套管理面动作；真实存储层满足它，
// 由 Service 在运行时断言取用，与网关装配处的能力断言同一口径。
type RequestLogReader interface {
	ListRequestLogs(ctx context.Context, f store.RequestLogFilter) ([]store.RequestLogRow, int, error)
	RequestLogByRequestIDUnscoped(ctx context.Context, requestID string) (*store.RequestLogRow, error)
	RequestAttempts(ctx context.Context, requestID string) ([]store.RequestAttempt, error)
	RequestStats(ctx context.Context, q store.RequestStatsQuery) ([]store.RequestStatsItem, error)
}

// requestLogReader 取回请求记录读路径的数据面。
func (s *Service) requestLogReader() (RequestLogReader, error) {
	reader, ok := s.store.(RequestLogReader)
	if !ok {
		return nil, errors.New("admin: 存储层未实现请求记录读取")
	}
	return reader, nil
}

// RequestListQuery 是请求记录列表的过滤与分页条件。
//
// 各过滤项与存储层同名同义：AccountID 为 0 表示不限账户；时间区间为闭区间；
// 空串与 0 表示不过滤。
type RequestListQuery struct {
	AccountID      uint64
	Since          time.Time
	Until          time.Time
	RequestedModel string
	RequestID      string
	Status         string
	APIKeyID       uint64
	Limit          int
	Offset         int
}

// ListRequests 分页列出请求记录，账户为 0 表示跨账户。
//
// 分页与终态在业务层再校验一次：服务层可被其它调用方直接使用，不能假设调用方
// 一定来自 CLI。
func (s *Service) ListRequests(ctx context.Context, q RequestListQuery) ([]store.RequestLogRow, int, error) {
	if q.Limit <= 0 {
		return nil, 0, invalidf("admin: 分页条数必须为正")
	}
	if q.Offset < 0 {
		return nil, 0, invalidf("admin: 分页偏移不能为负")
	}
	if q.Status != "" {
		if _, ok := requestStatuses[q.Status]; !ok {
			return nil, 0, invalidf("admin: 终态取值必须是 success / failed / cancelled")
		}
	}
	reader, err := s.requestLogReader()
	if err != nil {
		return nil, 0, err
	}
	return reader.ListRequestLogs(ctx, store.RequestLogFilter{
		AccountID:      q.AccountID,
		Since:          q.Since,
		Until:          q.Until,
		RequestedModel: q.RequestedModel,
		RequestID:      q.RequestID,
		Status:         q.Status,
		APIKeyID:       q.APIKeyID,
		Limit:          q.Limit,
		Offset:         q.Offset,
	})
}

// RequestDetailView 是一条请求记录的完整视图：脱敏报文与尝试时间线。
type RequestDetailView struct {
	Record   store.RequestLogRow
	Attempts []store.RequestAttempt
}

// RequestDetail 读一条请求记录及其尝试时间线，不带账户作用域。
//
// 不存在与不属于任何账户都是同一件事：管理面看的是全平台，没有越权面可分。
func (s *Service) RequestDetail(ctx context.Context, requestID string) (*RequestDetailView, error) {
	if err := requireString("请求标识", requestID); err != nil {
		return nil, err
	}
	reader, err := s.requestLogReader()
	if err != nil {
		return nil, err
	}
	row, err := reader.RequestLogByRequestIDUnscoped(ctx, requestID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundf("请求记录不存在")
		}
		return nil, err
	}
	attempts, err := reader.RequestAttempts(ctx, requestID)
	if err != nil {
		return nil, err
	}
	return &RequestDetailView{Record: *row, Attempts: attempts}, nil
}

// RequestStatsQuery 是请求计数聚合的过滤与分组条件。
//
// GroupBy 缺省为 day；account 维度只对管理面有意义（账户面恒为单账户）。
type RequestStatsQuery struct {
	AccountID      uint64
	Since          time.Time
	Until          time.Time
	RequestedModel string
	APIKeyID       uint64
	GroupBy        string
}

// RequestStats 按维度聚合请求计数，账户为 0 表示跨账户。
func (s *Service) RequestStats(ctx context.Context, q RequestStatsQuery) ([]store.RequestStatsItem, error) {
	if q.GroupBy == "" {
		q.GroupBy = "day"
	}
	if _, ok := requestStatsGroups[q.GroupBy]; !ok {
		return nil, invalidf("admin: 分组维度必须是 day / model / status / account")
	}
	reader, err := s.requestLogReader()
	if err != nil {
		return nil, err
	}
	return reader.RequestStats(ctx, store.RequestStatsQuery{
		AccountID:      q.AccountID,
		Since:          q.Since,
		Until:          q.Until,
		RequestedModel: q.RequestedModel,
		APIKeyID:       q.APIKeyID,
		GroupBy:        q.GroupBy,
	})
}
