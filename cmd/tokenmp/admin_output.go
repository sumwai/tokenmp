package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// 本文件是管理面子命令的输出与解析公共件：对齐表格、JSON 输出、时间与可空值格式化。
//
// 输出口径：默认对齐的纯文本表格，`--json` 时输出缩进 JSON。两者取自同一份数据，
// 表头与 JSON 字段不分别维护，避免同一列在两处漂移。

// tableGap 是表格列之间的空白，至少两个空格便于肉眼切列。
const tableGap = "  "

// adminTimeLayouts 是管理面接受的时间输入格式，长的在前避免误匹配。
var adminTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parseAdminTime 解析管理面的时间参数；空串返回零值时间。
func parseAdminTime(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, nil
	}
	for _, layout := range adminTimeLayouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("时间 %q 无法解析，接受 RFC3339、2006-01-02 15:04:05 或 2006-01-02", value)
}

// formatTime 格式化时间；零值输出占位符，避免表格里出现 0001-01-01。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

// formatTimePtr 格式化可空时间。
func formatTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return formatTime(*t)
}

// formatBool 把布尔值渲染成表格可读的中文。
func formatBool(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

// formatUintPtr 格式化可空无符号整数。
func formatUintPtr(value *uint64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatUint(*value, 10)
}

// emit 输出一次动作的结果。asJSON 为真时输出缩进 JSON，否则输出对齐表格。
//
// 表格与 JSON 共用同一份数据：调用方只负责把库表行映射成表格单元与可序列化载荷。
func (e *adminEnv) emit(asJSON bool, headers []string, rows [][]string, payload any) int {
	if asJSON {
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return e.fail(fmt.Errorf("编码 JSON 输出失败: %w", err))
		}
		if _, err := fmt.Fprintln(e.stdout, string(encoded)); err != nil {
			return e.fail(err)
		}
		return exitOK
	}
	if err := writeTable(e.stdout, headers, rows); err != nil {
		return e.fail(err)
	}
	return exitOK
}

// printf 写一行人类可读的结果；写失败按运行失败处理。
func (e *adminEnv) printf(format string, args ...any) int {
	if _, err := fmt.Fprintf(e.stdout, format, args...); err != nil {
		return e.fail(err)
	}
	return exitOK
}

// writeTable 把表头与行渲染成对齐的纯文本表格。
//
// 没有行时只打印表头，不额外提示：空结果本身是有效事实（该查询确实没有记录），
// 加一句「无数据」会让人分不清是查询成功还是查询没跑。
func writeTable(w io.Writer, headers []string, rows [][]string) error {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = displayWidth(header)
	}
	for _, row := range rows {
		for i := range headers {
			if i < len(row) && displayWidth(row[i]) > widths[i] {
				widths[i] = displayWidth(row[i])
			}
		}
	}

	var b strings.Builder
	writeRow(&b, headers, widths)
	for _, row := range rows {
		writeRow(&b, row, widths)
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("写出表格失败: %w", err)
	}
	return nil
}

// writeRow 写一行并对齐到列宽，去掉行尾空白。
func writeRow(b *strings.Builder, cells []string, widths []int) {
	for i, width := range widths {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		if i > 0 {
			b.WriteString(tableGap)
		}
		b.WriteString(cell)
		if pad := width - displayWidth(cell); pad > 0 && i < len(widths)-1 {
			b.WriteString(strings.Repeat(" ", pad))
		}
	}
	b.WriteString("\n")
}

// displayWidth 返回字符串的显示宽度。
//
// 只按 rune 计数，不处理中文的等宽双宽问题：该位置的取值大多是数字、标识符与
// ASCII 枚举，中文出现在少数名称列，轻微不齐不影响按列读值。
func displayWidth(value string) int {
	return len([]rune(value))
}
