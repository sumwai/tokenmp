package user

import (
	"net/http"

	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现账户面的模型目录。
//
// 只回答「这个账户能用哪些模型、它们以哪些协议方言服务」：上游模型名、
// 渠道标识与定价都不进响应，它们属运营口径。

// modelInfoView 是一个可调用模型，字段与契约 ModelInfo 对齐。
type modelInfoView struct {
	// Name 是客户端可用的模型名，可直接用于请求体。
	Name string `json:"name"`
	// Protocols 是服务该模型的上游协议方言。
	Protocols []string `json:"protocols"`
}

// handleModels 列出当前账户可调用的模型。
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	models, err := h.store.ListAccountModels(r.Context(), account.ID)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "模型目录查询失败")
		return
	}
	items := make([]modelInfoView, 0, len(models))
	for _, m := range models {
		items = append(items, modelInfoView{Name: m.Name, Protocols: m.Protocols})
	}
	// 目录不分页，但分页三字段照填：契约把 data 声明为 PageOfModelInfo，
	// 填「一页含全部」而不是留 null，前端的列表渲染与其余端点共用一条路径。
	total := len(items)
	webapi.WritePage(w, map[string]any{itemsKey: items}, 1, total, total)
}
