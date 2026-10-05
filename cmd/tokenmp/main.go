// Command tokenmp 是 TokenMP 的唯一入口二进制。
//
// 子命令在此集中分发，各子命令的实现放在同包内的独立文件里：
// 这样包级变量（如构建期注入的 version）只有一份，注入点不会随子命令数量扩散。
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// 帮助入口的三个形态；一并声明避免字面量在入口之间漂移。
const (
	helpName      = "help"
	helpShortFlag = "-h"
	helpLongFlag  = "--help"
)

// programName 同时用作子命令提示里的程序名与版本行的前缀。
const programName = "tokenmp"

// exitOK、exitUsage、exitFailure 是本程序的退出码口径。
//
// 用法错误与运行失败分开：脚本可以据此区分「命令写错了」与「命令跑了但失败了」，
// 前者不该重试，后者可能值得重试。
const (
	exitOK      = 0
	exitUsage   = 2
	exitFailure = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 分发子命令并返回退出码。参数与输出流都显式传入，便于测试覆盖各分支。
func run(args []string, stdout, stderr io.Writer) int {
	return runWithStdin(args, os.Stdin, stdout, stderr)
}

// runWithStdin 与 run 同义，额外接收标准输入：管理面的 credential add 与
// calendar import 需要从标准输入读内容。测试可注入受控输入。
func runWithStdin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usageWithExit(stderr, exitUsage)
	}

	switch args[0] {
	case "serve":
		return cmdServe(stderr)
	case "version":
		return cmdVersion(stdout, stderr)
	case "admin":
		return cmdAdmin(args[1:], stdin, stdout, stderr)
	case helpName, helpShortFlag, helpLongFlag:
		return usageWithExit(stdout, exitOK)
	default:
		// 提示与用法都写 stderr。写失败不改变退出码：进程已在退出路径上，
		// 没有补救手段，而退出码本身已在表达「命令用错了」。
		_, _ = fmt.Fprintf(stderr, "未知子命令 %q\n\n", args[0])
		return usageWithExit(stderr, exitUsage)
	}
}

// usageWithExit 打印用法并返回指定退出码。
//
// 用法文本写失败时仍返回 code 而不是失败码：code 携带的信息更具体
// （「命令用错了」还是「成功」），叠加一个通用失败码只会把它抹掉。
func usageWithExit(w io.Writer, code int) int {
	_, _ = io.WriteString(w, usageText())
	return code
}

// usageText 返回子命令清单。整段一次成串，避免逐行写入在中途失败时留下半截输出。
func usageText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "用法：%s <子命令>\n\n", programName)
	b.WriteString("子命令：\n")
	b.WriteString("  serve      启动网关 HTTP 服务\n")
	b.WriteString("  admin      管理面：商家、渠道、账户、定价与充值\n")
	b.WriteString("  version    报出版本号、构建自哪个提交，以及运行时的 Go 版本\n")
	b.WriteString("  help       打印本帮助\n")
	return b.String()
}
