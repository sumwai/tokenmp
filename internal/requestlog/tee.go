package requestlog

import (
	"context"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/transport"
)

// 本文件把「一条事实交给两个落点」显式写成一个类型，而不是在各实现里互相调用：
// 日志出口与留存出口互不认识，任一个换成别的实现都不影响另一个，装配层只负责配对。
//
// 两份落点的失败语义一致：观测是转发的旁路，任何一份失败都不改变转发结果，
// 因此副本总是先写、错误总是忽略，主实现的返回值原样上交。

// ObserverTee 把每次上游尝试同时交给两个 Observer。
type ObserverTee struct {
	// Primary 是主落点，其返回值上交给调用方。
	Primary domain.Observer
	// Secondary 是副本落点，错误被忽略。
	Secondary domain.Observer
}

var _ domain.Observer = ObserverTee{}

// RecordAttempt 先写副本再写主落点。
func (t ObserverTee) RecordAttempt(ctx context.Context, rec domain.AttemptRecord) error {
	if t.Secondary != nil {
		_ = t.Secondary.RecordAttempt(ctx, rec)
	}
	if t.Primary == nil {
		return nil
	}
	return t.Primary.RecordAttempt(ctx, rec)
}

// UsageTee 把每次进入终态的用量同时交给两个 UsageRecorder。
type UsageTee struct {
	Primary   domain.UsageRecorder
	Secondary domain.UsageRecorder
}

var _ domain.UsageRecorder = UsageTee{}

// RecordUsage 先写副本再写主落点。
func (t UsageTee) RecordUsage(ctx context.Context, rec domain.UsageRecord) error {
	if t.Secondary != nil {
		_ = t.Secondary.RecordUsage(ctx, rec)
	}
	if t.Primary == nil {
		return nil
	}
	return t.Primary.RecordUsage(ctx, rec)
}

// AccessTee 把每条访问记录同时交给两个 AccessLogger。
type AccessTee struct {
	Primary   transport.AccessLogger
	Secondary transport.AccessLogger
}

var _ transport.AccessLogger = AccessTee{}

// LogAccess 先写副本再写主落点。
func (t AccessTee) LogAccess(ctx context.Context, rec transport.AccessRecord) {
	if t.Secondary != nil {
		t.Secondary.LogAccess(ctx, rec)
	}
	if t.Primary != nil {
		t.Primary.LogAccess(ctx, rec)
	}
}
