package admin

import (
	"context"

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
type ChannelInput struct {
	MerchantID uint64
	Name       string
	Vendor     string
	Type       store.ChannelType
	CredGroup  string
	BaseURL    string
	Priority   int
	Weight     int
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
	return s.store.InsertChannel(ctx, store.Channel{
		MerchantID: in.MerchantID,
		Name:       in.Name,
		Vendor:     in.Vendor,
		Type:       in.Type,
		CredGroup:  in.CredGroup,
		BaseURL:    in.BaseURL,
		Priority:   in.Priority,
		Weight:     in.Weight,
	})
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
