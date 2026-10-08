package webapi

import "strings"

// BearerToken 从 Authorization 头取令牌；方案名不区分大小写，令牌非空。
//
// 页面面的认证面与用户业务面共用这一份解析：两边的凭据是同一个，语义没有
// 分叉的余地。与 internal/access 的同名解析刻意各写一份，因为那是另一套凭据
// （数据面 API 密钥），演化方向不同。
func BearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}
