package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// 本文件是探针 JSON 路径的最小实现：支持 $.a.b[0].c 形态的取值。
//
// 刻意不引入通用 JSONPath 库：探针配置由本仓库的使用方书写，需要的只是「点号分段 + 数组下标」，
// 引入一套完整的过滤器语法会换来一份读不完的规范与一批用不到的依赖。

// decodeJSONDocument 把 JSON 字节解析成通用结构。
//
// 用 UseNumber：JSON 数字保持原始十进制文本，转换成 decimal 时不经过 float64。
func decodeJSONDocument(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("plan: 探针响应不是合法 JSON: %w", err)
	}
	return document, nil
}

// pathToken 是 JSON 路径的一段：对象键或数组下标。
type pathToken struct {
	key     string
	index   int
	isIndex bool
}

// lookupJSONPath 按路径从已解析的 JSON 文档里取出一个数值。
//
// 第二个返回值为 false 表示路径不存在、类型不匹配或叶子不是数值。
// 叶子是 JSON null 时返回 0 与 true：显式 null 表示「本窗口没有用量」，是合法取值。
func lookupJSONPath(document any, path string) (decimal.Decimal, bool) {
	tokens, ok := parseJSONPath(path)
	if !ok {
		return decimal.Zero, false
	}
	current := document
	for _, token := range tokens {
		if token.isIndex {
			array, ok := current.([]any)
			if !ok || token.index < 0 || token.index >= len(array) {
				return decimal.Zero, false
			}
			current = array[token.index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return decimal.Zero, false
		}
		value, exists := object[token.key]
		if !exists {
			return decimal.Zero, false
		}
		current = value
	}
	return decimalFromJSON(current)
}

// parseJSONPath 把 $.a.b[0].c 解析成路径段；非法路径返回 false。
func parseJSONPath(path string) ([]pathToken, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(path), "$")
	if !ok {
		return nil, false
	}
	var tokens []pathToken
	for rest != "" {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			var key string
			if end < 0 {
				key, rest = rest, ""
			} else {
				key, rest = rest[:end], rest[end:]
			}
			if key == "" {
				return nil, false
			}
			tokens = append(tokens, pathToken{key: key})
		case '[':
			closeAt := strings.IndexByte(rest, ']')
			if closeAt < 0 {
				return nil, false
			}
			index, err := strconv.Atoi(strings.TrimSpace(rest[1:closeAt]))
			if err != nil || index < 0 {
				return nil, false
			}
			tokens = append(tokens, pathToken{index: index, isIndex: true})
			rest = rest[closeAt+1:]
		default:
			return nil, false
		}
	}
	if len(tokens) == 0 {
		return nil, false
	}
	return tokens, true
}

// decimalFromJSON 把 JSON 叶子转成 decimal；非数值返回 false。
func decimalFromJSON(value any) (decimal.Decimal, bool) {
	switch v := value.(type) {
	case json.Number:
		parsed, err := decimal.NewFromString(v.String())
		if err != nil {
			return decimal.Zero, false
		}
		return parsed, true
	case string:
		parsed, err := decimal.NewFromString(strings.TrimSpace(v))
		if err != nil {
			return decimal.Zero, false
		}
		return parsed, true
	case nil:
		return decimal.Zero, true
	default:
		return decimal.Zero, false
	}
}
