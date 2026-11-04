package eportal

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	devicesURL = baseURL + "/eportal/adaptor/devices/findDevice"
	kickURL    = baseURL + "/eportal/adaptor/kick-offline/batch"
)

// Device 是当前在线账号的一台设备；UUID 是门户提供的下线标识。
type Device struct {
	UUID           string `json:"uuid"`
	Name           string `json:"name"`
	IP             string `json:"ip,omitempty"`
	MAC            string `json:"mac,omitempty"`
	Type           string `json:"type,omitempty"`
	AuthType       string `json:"auth_type,omitempty"`
	OnlineDuration string `json:"online_duration,omitempty"`
	Current        bool   `json:"current"`
}

type DeviceList struct {
	Online []Device       `json:"online"`
	Raw    map[string]any `json:"raw,omitempty"`
}

// ListDevices 沿用网页的在线会话查设备；不读取本地密码或 CAS 凭据。
func (c *Client) ListDevices(ctx context.Context) (*DeviceList, error) {
	sessionID, status, err := c.onlineSession(ctx)
	if err != nil {
		return nil, err
	}
	return c.devicesForSession(ctx, sessionID, status)
}

func (c *Client) devicesForSession(ctx context.Context, sessionID string, status *OnlineStatus) (*DeviceList, error) {
	data, err := c.postJSON(ctx, devicesURL, map[string]string{"sessionId": sessionID})
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: bad devices data: %v", ErrProtocol, err)
	}
	if _, ok := raw["onlineDevices"]; !ok {
		return nil, fmt.Errorf("%w: devices data missing onlineDevices", ErrProtocol)
	}
	var payload struct {
		OnlineDevices []struct {
			UUID           string `json:"onlineUserUuid"`
			Name           string `json:"deviceName"`
			IP             string `json:"userIpv4"`
			MAC            string `json:"userMac"`
			Type           string `json:"deviceType"`
			AuthType       string `json:"authType"`
			OnlineDuration string `json:"onlineDuration"`
			Current        bool   `json:"currentDevice"`
		} `json:"onlineDevices"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: bad onlineDevices data: %v", ErrProtocol, err)
	}
	list := &DeviceList{Online: make([]Device, 0, len(payload.OnlineDevices)), Raw: raw}
	seen := make(map[string]bool)
	for _, device := range payload.OnlineDevices {
		uuid := strings.ToLower(device.UUID)
		if !deviceUUID.MatchString(uuid) || seen[uuid] {
			return nil, fmt.Errorf("%w: onlineDevices contains an invalid or duplicate UUID", ErrProtocol)
		}
		seen[uuid] = true
		current := device.Current || device.IP != "" && device.IP == status.UserIP ||
			device.MAC != "" && normalizeMAC(device.MAC) == normalizeMAC(status.UserMAC)
		list.Online = append(list.Online, Device{
			UUID: device.UUID, Name: device.Name, IP: device.IP, MAC: device.MAC,
			Type: device.Type, AuthType: device.AuthType, OnlineDuration: device.OnlineDuration,
			Current: current,
		})
	}
	return list, nil
}

func normalizeMAC(mac string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac))
}

var deviceUUID = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// resolveDeviceTargets 在发出任何下线请求前一次性校验所有目标并去重。
func resolveDeviceTargets(targets []string, devices []Device) ([]Device, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("下线目标不能为空")
	}
	byUUID := make(map[string]Device, len(devices))
	for _, device := range devices {
		byUUID[strings.ToLower(device.UUID)] = device
	}
	seen := make(map[string]bool)
	var selected []Device
	for _, target := range targets {
		var device Device
		if deviceUUID.MatchString(target) {
			var ok bool
			device, ok = byUUID[strings.ToLower(target)]
			if !ok {
				return nil, fmt.Errorf("UUID %s 不在当前账号的在线设备列表中", target)
			}
		} else if number, err := strconv.Atoi(target); err == nil {
			if number < 1 || number > len(devices) {
				return nil, fmt.Errorf("序号 %d 超出范围（当前在线 %d 台）", number, len(devices))
			}
			device = devices[number-1]
		} else {
			return nil, fmt.Errorf("无法识别的目标 %q：请用 devices 输出中的序号或 UUID", target)
		}
		key := strings.ToLower(device.UUID)
		if !seen[key] {
			selected = append(selected, device)
			seen[key] = true
		}
	}
	return selected, nil
}

// KickDevices 查询并校验当前账号设备，使用同一个流程会话提交下线请求。
// 门户执行下线可能有延迟；返回成功表示请求被接受，不表示目标已经离线。
func (c *Client) KickDevices(ctx context.Context, targets []string) ([]Device, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("下线目标不能为空")
	}
	sessionID, status, err := c.onlineSession(ctx)
	if err != nil {
		return nil, err
	}
	list, err := c.devicesForSession(ctx, sessionID, status)
	if err != nil {
		return nil, err
	}
	selected, err := resolveDeviceTargets(targets, list.Online)
	if err != nil {
		return nil, err
	}
	uuids := make([]string, 0, len(selected))
	for _, device := range selected {
		uuids = append(uuids, device.UUID)
	}
	body, err := json.Marshal(map[string]any{"sessionId": sessionID, "onlineUserUuids": uuids})
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.PostJSON(ctx, kickURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("下线请求未确认，请用 devices 检查: %w", err)
	}
	env, err := c.readEnvelope(resp, kickURL)
	if err != nil {
		return nil, err
	}
	// 与网页相同：除 code=200 外，还必须得到明确的 ok/OK。
	if !strings.EqualFold(env.Message, "ok") {
		return nil, fmt.Errorf("%w: 下线请求未被确认：%s", ErrBusiness, orDefault(env.Message, "响应缺少确认信息"))
	}
	return selected, nil
}
