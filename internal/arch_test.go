package internal_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// internalPrefix 是仓库内部包的导入前缀。
const internalPrefix = "github.com/sumwai/tokenmp/internal/"

// 测试二进制在包目录（internal/）下运行，因此下列路径均相对于 internal/。

// TestInternalLayoutExists 守护 `internal/` 下已建的分层目录结构，标为规划但尚未创建的目录不在本断言范围内。
//
// 清单只列转发内核的分层目录，且只列本仓库真实存在的那些：router 属装配面，
// 未随转发内核移植，列入只会让本断言永远失败；billing、store、config 虽在本仓库存在，
// 但不属转发内核的分层，不在本断言范围内。transport 下有 sse 子目录、
// adapters 下按协议划分若干子目录，子目录随父目录一并被其它扫描类守卫覆盖。
func TestInternalLayoutExists(t *testing.T) {
	for _, dir := range []string{"domain", "pipeline", "adapters", "transport", "upstream"} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("缺少分层目录 internal/%s: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("internal/%s 不是目录", dir)
		}
	}
}

// TestDomainHasNoUpperLayerImports 守护分层红线：
// domain 是最底层，不得导入任何其它 internal 包（标准库与仓库外依赖不在此断言范围）。
//
// 递归扫描 internal/domain 下的全部 Go 文件。domain 已落地统一内部协议，
// 因此必须至少检查到 1 个 Go 文件；检查到 0 个文件说明目录被清空或路径出错，直接失败。
func TestDomainHasNoUpperLayerImports(t *testing.T) {
	const domainDir = "domain"
	checked := 0

	err := filepath.WalkDir(domainDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		checked++

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("解析 %s 失败: %w", path, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("解析 %s 中的导入路径 %s 失败: %w", path, spec.Path.Value, err)
			}
			if strings.HasPrefix(importPath, internalPrefix) {
				t.Errorf("%s 导入了 %s：domain 为最底层，不得导入任何其它 internal 包", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("internal/domain 下没有 Go 文件：统一内部协议应已落地")
	}
	t.Logf("已检查 internal/domain 下 %d 个 Go 文件", checked)
}

// TestAdaptersHaveNoSSEFraming 守护「从字节流切分 SSE 帧只有 internal/transport/sse 一份实现」：
// internal/adapters 下的生产文件（非 _test.go）不得引入 bufio，也不得引用 io.Reader。
// 适配器只暴露逐帧解码入口（DecodeStreamFrame 接收单帧的事件名与 data 字节），
// 自带分帧实现属多套相似实现。
func TestAdaptersHaveNoSSEFraming(t *testing.T) {
	const adaptersDir = "adapters"

	fset := token.NewFileSet()
	checked := 0

	err := filepath.WalkDir(adaptersDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		checked++

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("解析 %s 失败: %w", path, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			if importPath == "bufio" {
				t.Errorf("%s 导入了 bufio：从字节流切分 SSE 帧属 internal/transport/sse 的职责", path)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Reader" {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "io" {
				t.Errorf("%s:%d 引用了 io.Reader：适配器不得自带基于流读取器的 SSE 分帧实现",
					path, fset.Position(sel.Pos()).Line)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("internal/adapters 下没有生产代码文件，扫描失去意义")
	}
	t.Logf("已检查 internal/adapters 下 %d 个生产代码文件", checked)
}

// protocolOnlyLiterals 是各上游协议专有的字面量：结束原因取值与请求体字段名。
//
// 它们必须由各自的适配器映射或构造。
// 一旦出现在 domain，新增协议就要改动统一协议包。
// `model` 不以独立字面量登记：`internal/domain` 的 `Request`/`Response` 用它作为自身的 JSON 标签，属内部字段名；
// `max_tokens` 在 `internal/domain` 仅以 `max_tokens,omitempty` 标签形态出现，与独立字面量不等值，
// 本表只拦截独立字面量。
var protocolOnlyLiterals = []string{
	"end_turn", "stop_sequence", "tool_use", "function_call",
	"max_output_tokens", "incomplete", "refusal", "completed",
	"max_tokens", "max_completion_tokens", "stream_options", "include_usage",
}

// forwardingLayers 是必须对协议无感知的共享转发层目录：唯一转发流水线与通用 HTTP 入口。
var forwardingLayers = []string{"pipeline", "transport"}

// protocolPathLiterals 是三种线协议的规范请求路径。路由由装配层注入，共享转发层
// 一旦出现协议路径字面量，就等于按协议硬编码了路由。
var protocolPathLiterals = []string{
	"/v1/chat/completions", "/v1/responses", "/v1/messages",
}

// protocolFieldLiterals 是三种协议专有的请求体字段名（含跨协议通用的 `model`）。
// 共享转发层不得按协议构造或识别这些字段：字段级改写由适配器在 internal/domain
// 提供的协议无关函数上完成。
var protocolFieldLiterals = []string{
	"model", "max_tokens", "max_completion_tokens", "max_output_tokens",
	"stream_options", "include_usage",
}

// forwardingForbiddenImports 是共享转发层不得导入的实现包前缀。
//
// 共享转发层依赖的端口定义在 internal/domain：
//
// - Adapter
// - UpstreamCaller
// - RouteResolver
// - Observer
//
// 具体的实现由 cmd 装配层负责注入。
// 清单列本仓库真实存在的实现包：适配器、上游客户端、仓储（store）、计费（billing）、
// 配置（config）与应用装配（cmd）都在共享转发层之上，一旦被导入，就说明依赖方向被反转，必须拦下。
var forwardingForbiddenImports = []string{
	internalPrefix + "upstream",
	internalPrefix + "adapters",
	internalPrefix + "store",
	internalPrefix + "billing",
	internalPrefix + "config",
	"github.com/sumwai/tokenmp/cmd",
}

// TestForwardingLayersHaveNoImplementationImports 守护「端口注入、实现外置」：
//
// internal/pipeline 与 internal/transport 的生产代码不得导入以下实现包：
//
// - 上游客户端（upstream）
// - 适配器（adapters）
// - 仓储（store）
// - 计费（billing）
// - 配置（config）
// - 应用装配（cmd）
//
// 使「共享转发层只依赖 internal/domain」成为可被测试发现的约束。
func TestForwardingLayersHaveNoImplementationImports(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0

	for _, layer := range forwardingLayers {
		err := filepath.WalkDir(layer, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			checked++

			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return fmt.Errorf("解析 %s 失败: %w", path, err)
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					continue
				}
				for _, prefix := range forwardingForbiddenImports {
					if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
						t.Errorf("%s 导入了 %s：共享转发层的依赖由装配层注入，本层只依赖 internal/domain",
							path, importPath)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked == 0 {
		t.Fatal("共享转发层下没有生产代码文件，扫描失去意义")
	}
	t.Logf("已检查共享转发层 %d 个生产代码文件的依赖", checked)
}

// TestForwardingLayersHaveNoProtocolLiterals 守护共享转发层不得出现协议字面量：
// internal/pipeline 与 internal/transport 的生产代码不得出现任何协议名、协议路径或
// 协议专有字段名的字符串字面量，也不得出现它们的字符串常量拼接。协议名表从
// internal/domain 的 Protocol 常量 AST（Abstract Syntax Tree，抽象语法树）派生，
// 避免本测试与 domain 手工维护两份副本而漂移。
func TestForwardingLayersHaveNoProtocolLiterals(t *testing.T) {
	forbidden := append(domainProtocolNames(t), protocolPathLiterals...)
	forbidden = append(forbidden, protocolFieldLiterals...)

	fset := token.NewFileSet()
	checked := 0

	for _, layer := range forwardingLayers {
		err := filepath.WalkDir(layer, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			checked++

			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return fmt.Errorf("解析 %s 失败: %w", path, err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				switch expr := node.(type) {
				case *ast.BasicLit:
					if expr.Kind != token.STRING {
						return true
					}
					value, unquoteErr := strconv.Unquote(expr.Value)
					if unquoteErr != nil {
						return true
					}
					reportForbiddenLiteral(t, fset, path, expr.Pos(), value, forbidden)
				case *ast.BinaryExpr:
					if value, ok := foldedString(expr); ok {
						reportForbiddenLiteral(t, fset, path, expr.Pos(), value, forbidden)
						// 常量拼接已整体检查，不再深入子表达式，避免同一拼接被重复报告。
						return false
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked == 0 {
		t.Fatal("共享转发层下没有生产代码文件，扫描失去意义")
	}
	t.Logf("已检查共享转发层 %d 个生产代码文件", checked)
}

// reportForbiddenLiteral 报告字符串常量命中禁止字面量。
func reportForbiddenLiteral(t *testing.T, fset *token.FileSet, path string, pos token.Pos, value string, forbidden []string) {
	t.Helper()
	for _, item := range forbidden {
		if value == item {
			t.Errorf("%s:%d 出现协议字面量 %q：协议差异属适配器职责，路由由装配层注入",
				path, fset.Position(pos).Line, item)
		}
	}
}

// domainProtocolNames 扫描 internal/domain 的生产代码，返回声明类型为 Protocol 的
// 字符串常量取值。
func domainProtocolNames(t *testing.T) []string {
	t.Helper()

	fset := token.NewFileSet()
	var values []string
	checked := 0

	err := filepath.WalkDir("domain", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		checked++

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("解析 %s 失败: %w", path, err)
		}
		values = append(values, protocolConstsInFile(file)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 || len(values) == 0 {
		t.Fatal("未从 internal/domain 扫描到 Protocol 常量，协议字面量守卫失去依据")
	}
	return values
}

// protocolConstsInFile 返回单个 Go 文件里声明类型为 Protocol 的字符串常量取值。
// 分组 const 里后续省略类型声明的写法按 Go 的类型继承规则沿用上一个 spec 的类型。
func protocolConstsInFile(file *ast.File) []string {
	var values []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		declaredProtocol := false
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			switch {
			case valueSpec.Type == nil:
				// 省略类型声明：沿用上一个 spec 的类型。
			case isProtocolType(valueSpec.Type):
				declaredProtocol = true
			default:
				declaredProtocol = false
			}
			if !declaredProtocol {
				continue
			}
			for _, expr := range valueSpec.Values {
				if value, ok := foldedString(expr); ok {
					values = append(values, value)
				}
			}
		}
	}
	return values
}

// isProtocolType 报告表达式是否就是 domain 包内的 Protocol 类型标识符。
func isProtocolType(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "Protocol"
}

// foldedString 尝试把表达式折叠为字符串常量：字符串字面量，或字符串常量之间的
// `+` 拼接。折叠失败返回 false。用于识别 `"openai" + "_chat"` 这类绕过字面量等值比较的写法。
func foldedString(expr ast.Expr) (string, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		unquoted, err := strconv.Unquote(value.Value)
		if err != nil {
			return "", false
		}
		return unquoted, true
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return "", false
		}
		left, ok := foldedString(value.X)
		if !ok {
			return "", false
		}
		right, ok := foldedString(value.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	default:
		return "", false
	}
}

// TestDomainHasNoProtocolOnlyLiterals 守护「协议专有字面量不进入统一内部协议」：
// 扫描 internal/domain 生产代码的全部字符串字面量，命中即失败。
// 适配器落地后，各协议「未知字面量 → FinishUnknown」的测试由适配器自己负责。
func TestDomainHasNoProtocolOnlyLiterals(t *testing.T) {
	const domainDir = "domain"

	fset := token.NewFileSet()
	checked := 0

	err := filepath.WalkDir(domainDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		checked++

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("解析 %s 失败: %w", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, forbidden := range protocolOnlyLiterals {
				if value == forbidden {
					t.Errorf("%s:%d 出现协议专有字面量 %q：映射属适配器职责，不得放进统一协议包",
						path, fset.Position(lit.Pos()).Line, forbidden)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("internal/domain 下没有生产代码文件，扫描失去意义")
	}
	t.Logf("已检查 internal/domain 下 %d 个生产代码文件", checked)
}
