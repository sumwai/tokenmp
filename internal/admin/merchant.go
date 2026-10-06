package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是商家与渠道的管理动作。

// CreateMerchant 新建一个商家；新建一律 active，停用是独立动作。
//
// 入驻商家（partner）与平台自营走同一条路径，kind 只是数据：平台自营也是一行数据，
// 不是特例。
func (s *Service) CreateMerchant(ctx context.Context, code, name string, kind store.MerchantKind) (uint64, error) {
	if err := requireString("商家 code", code); err != nil {
		return 0, err
	}
	if err := requireString("商家 name", name); err != nil {
		return 0, err
	}
	if err := store.ValidateMerchantKind(kind); err != nil {
		return 0, err
	}
	return s.store.InsertMerchant(ctx, store.Merchant{
		Code: code, Name: name, Kind: kind, Status: store.StatusActive,
	})
}

// ListMerchants 列出全部商家。
func (s *Service) ListMerchants(ctx context.Context) ([]store.Merchant, error) {
	return s.store.ListMerchants(ctx)
}

// DisableMerchant 停用一个商家。
func (s *Service) DisableMerchant(ctx context.Context, id uint64) error {
	if err := requireID("商家 id", id); err != nil {
		return err
	}
	return s.store.SetMerchantStatus(ctx, id, store.StatusDisabled)
}

// 渠道路由参数的默认值，与 0001 迁移的列默认值一致。
const (
	defaultChannelPriority = 100
	defaultChannelWeight   = 100
)

// ChannelInput 是新建渠道的输入。
//
// Config 是渠道级扩展配置原始 JSON（探针声明即写在这里），空值表示不配置。
type ChannelInput struct {
	MerchantID uint64
	Name       string
	Vendor     string
	Type       store.ChannelType
	CredGroup  string
	BaseURL    string
	Priority   int
	Weight     int
	// Config 是渠道级扩展配置原始 JSON（探针声明即写在这里），空值表示不配置。
	Config string
	// CredentialStyle 覆盖该渠道凭据的注入形态；零值表示不配置，按协议现状注入。
	// 典型取值：query 表示改用 ?key=<凭据> 查询参数形态（兼容只接受查询参数的上游）。
	CredentialStyle domain.CredentialHeaderStyle
}

// configFieldCredentialStyle 是 upstream_channel.config 里凭据注入形态的键名，
// 与选路边界的读取口径同源。
const configFieldCredentialStyle = "credential_style" //nolint:gosec // G101：这是 config 键名，不是凭据。

// channelConfigJSON 合并渠道级扩展配置与凭据注入形态，编码为 config JSON。
//
// raw 是探针声明等扩展配置的原文，空值表示不配置；两者同时给出时并进同一个 JSON 对象。
// 非法注入形态在写入入口报错：config 里的写坏值只会让选路时静默回退到协议现状，
// 把「配错了」藏起来；写入是唯一能把它显式拦下的地方。
func channelConfigJSON(raw string, style domain.CredentialHeaderStyle) (json.RawMessage, error) {
	config, err := channelConfigArg(raw)
	if err != nil {
		return nil, err
	}
	if style == domain.CredentialHeaderAuto {
		return config, nil
	}
	if !style.Valid() {
		return nil, fmt.Errorf("admin: 凭据注入形态 %q 不受支持", string(style))
	}
	object := map[string]json.RawMessage{}
	if len(config) > 0 {
		if uerr := json.Unmarshal(config, &object); uerr != nil {
			return nil, errors.New("渠道 config 必须是 JSON 对象")
		}
	}
	encoded, err := json.Marshal(string(style))
	if err != nil {
		return nil, fmt.Errorf("admin: 编码渠道配置失败: %w", err)
	}
	object[configFieldCredentialStyle] = encoded
	merged, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("admin: 编码渠道配置失败: %w", err)
	}
	return json.RawMessage(merged), nil
}

// CreateChannel 新建一条渠道。
//
// 协议方言决定数据面的分发键，必须走 store 白名单；priority / weight 为 0 时
// 取列默认值 100，避免把「没配」写成 0（0 会让该渠道永远排在最后）。
func (s *Service) CreateChannel(ctx context.Context, in ChannelInput) (uint64, error) {
	if err := requireID("渠道 merchant", in.MerchantID); err != nil {
		return 0, err
	}
	if err := requireString("渠道 name", in.Name); err != nil {
		return 0, err
	}
	if err := store.ValidateChannelType(in.Type); err != nil {
		return 0, err
	}
	if err := requireString("渠道 cred-group", in.CredGroup); err != nil {
		return 0, err
	}
	if err := requireString("渠道 base-url", in.BaseURL); err != nil {
		return 0, err
	}
	if in.Priority == 0 {
		in.Priority = defaultChannelPriority
	}
	if in.Weight == 0 {
		in.Weight = defaultChannelWeight
	}
	config, err := channelConfigJSON(in.Config, in.CredentialStyle)
	if err != nil {
		return 0, err
	}
	return s.store.InsertChannel(ctx, store.Channel{
		MerchantID: in.MerchantID,
		Name:       in.Name,
		Vendor:     in.Vendor,
		Type:       in.Type,
		CredGroup:  in.CredGroup,
		BaseURL:    in.BaseURL,
		Priority:   in.Priority,
		Weight:     in.Weight,
		Config:     config,
	})
}

// channelConfigArg 校验渠道扩展配置是 JSON 对象，空值表示不配置。
func channelConfigArg(raw string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return nil, errors.New("渠道 config 必须是 JSON 对象")
	}
	if object == nil {
		return nil, errors.New("渠道 config 必须是 JSON 对象")
	}
	return json.RawMessage(trimmed), nil
}

// ListChannels 列出全部渠道。
func (s *Service) ListChannels(ctx context.Context) ([]store.Channel, error) {
	return s.store.ListChannels(ctx)
}

// EnableChannel 启用一条渠道。
func (s *Service) EnableChannel(ctx context.Context, id uint64) error {
	return s.setChannelEnabled(ctx, id, true)
}

// DisableChannel 停用一条渠道。
func (s *Service) DisableChannel(ctx context.Context, id uint64) error {
	return s.setChannelEnabled(ctx, id, false)
}

// setChannelEnabled 是启用 / 停用的公共校验与调用。
func (s *Service) setChannelEnabled(ctx context.Context, id uint64, enabled bool) error {
	if err := requireID("渠道 id", id); err != nil {
		return err
	}
	return s.store.SetChannelEnabled(ctx, id, enabled)
}
