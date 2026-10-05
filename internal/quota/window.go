package quota

import (
	"fmt"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件是窗口计算：把 window_kind 与 period 解释成「从哪一刻开始算已用量」。
//
// 计算只依赖 now 与枚举，是纯函数，因此窗口边界可以按跨月、跨年、跨周边界做
// 表驱动覆盖，不需要数据库或时钟替身。

const (
	// period5h 是滚动窗口的固定时长，与 billing.Period5h 的语义对应。
	period5h = 5 * time.Hour
	// weekdayCount 是一周的天数，用于把 time.Weekday 的取值折成周内偏移。
	weekdayCount = 7
)

// WindowStart 返回一条限额当前窗口的起点。
//
// ok 为 false 表示 window_kind 与 period 的组合没有可判定的窗口（如 calendar×5h）：
// 调用方应跳过该限额，而不是退化成全时段聚合。
//
// period=total 返回零值时间且 ok 为 true：零值表示不做窗口下界，聚合范围只受
// account_quota_event 的 reset 基准约束。
//
// 自然周期按 now 的时区求边界；周起点为周一。时区取自进程时钟而不是数据库：
// 判定必须在测试里可复现，且与 created_at 的写入方共用同一套时钟口径。
func WindowStart(kind billing.WindowKind, period billing.Period, now time.Time) (time.Time, bool) {
	switch period {
	case billing.Period5h:
		if kind != billing.WindowKindRolling {
			return time.Time{}, false
		}
		return now.Add(-period5h), true
	case billing.PeriodDay:
		if kind != billing.WindowKindCalendar {
			return time.Time{}, false
		}
		return startOfDay(now), true
	case billing.PeriodWeek:
		if kind != billing.WindowKindCalendar {
			return time.Time{}, false
		}
		return startOfWeek(now), true
	case billing.PeriodMonth:
		if kind != billing.WindowKindCalendar {
			return time.Time{}, false
		}
		return startOfMonth(now), true
	case billing.PeriodTotal:
		// 总量口径不依赖 window_kind：没有周期边界，聚合只受 reset 基准约束。
		return time.Time{}, true
	default:
		return time.Time{}, false
	}
}

// ValidWindow 报告 window_kind 与 period 是否为受支持的组合，不受支持时返回原因。
//
// 写入口用它拦住永远不会被判定的组合；判定入口用 WindowStart 的 ok 跳过历史脏行，
// 两端共用同一张组合表。
func ValidWindow(kind billing.WindowKind, period billing.Period) error {
	if err := billing.ValidateWindowKind(kind); err != nil {
		return err
	}
	if err := billing.ValidatePeriod(period); err != nil {
		return err
	}
	if _, ok := WindowStart(kind, period, time.Time{}); !ok {
		return fmt.Errorf("quota: window_kind %q 与 period %q 不是受支持的组合", string(kind), string(period))
	}
	return nil
}

// RetryAfter 返回 throttle 处置下建议的退避时长：当前窗口结束、已用量随之移出
// 窗口所需的时间。
//
// rolling 没有固定边界，取一个完整周期作为保守估计；total 不会自然重置，返回 0，
// 由调用方省略 Retry-After 头。
func RetryAfter(kind billing.WindowKind, period billing.Period, now time.Time) time.Duration {
	switch period {
	case billing.Period5h:
		if kind != billing.WindowKindRolling {
			return 0
		}
		return period5h
	case billing.PeriodDay:
		if kind != billing.WindowKindCalendar {
			return 0
		}
		return startOfDay(now).AddDate(0, 0, 1).Sub(now)
	case billing.PeriodWeek:
		if kind != billing.WindowKindCalendar {
			return 0
		}
		return startOfWeek(now).AddDate(0, 0, weekdayCount).Sub(now)
	case billing.PeriodMonth:
		if kind != billing.WindowKindCalendar {
			return 0
		}
		return startOfMonth(now).AddDate(0, 1, 0).Sub(now)
	default:
		return 0
	}
}

// startOfDay 返回当日零点。
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// startOfWeek 返回所在自然周的周一零点。
func startOfWeek(t time.Time) time.Time {
	// time.Weekday 里周日为 0；折成「距本周一的天数」后周日得到 6。
	offset := (int(t.Weekday()) + weekdayCount - 1) % weekdayCount
	return startOfDay(t).AddDate(0, 0, -offset)
}

// startOfMonth 返回当月一日零点。
func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}
