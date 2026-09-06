package config

import (
	"fmt"
	"time"
)

// 北京时间固定为 UTC+8，不依赖宿主机时区或路由器上的 zoneinfo。
var beijing = time.FixedZone("UTC+08:00", 8*60*60)

// NoAuthPeriod 指定每周重复的禁认证时段。Weekdays 是开始日，0=周日，6=周六。
// Start/End 使用北京时间 HH:MM；结束早于开始时跨至次日，区间左闭右开。
type NoAuthPeriod struct {
	Enabled  bool   `json:"enabled"`
	Weekdays []int  `json:"weekdays"`
	Start    string `json:"start"`
	End      string `json:"end"`
}

func (p NoAuthPeriod) validate() error {
	if !p.Enabled {
		return nil
	}
	if len(p.Weekdays) == 0 {
		return fmt.Errorf("config: daemon.no_auth_period.weekdays must not be empty")
	}
	for _, day := range p.Weekdays {
		if day < 0 || day > 6 {
			return fmt.Errorf("config: daemon.no_auth_period.weekdays must be between 0 (Sunday) and 6 (Saturday)")
		}
	}
	for _, field := range []struct{ name, value string }{{"start", p.Start}, {"end", p.End}} {
		if _, err := time.Parse("15:04", field.value); err != nil || len(field.value) != 5 {
			return fmt.Errorf("config: daemon.no_auth_period.%s must use HH:MM (00:00–23:59)", field.name)
		}
	}
	if p.Start == p.End {
		return fmt.Errorf("config: daemon.no_auth_period.start and end must differ")
	}
	return nil
}

// NextWindow 返回包含 now 或位于 now 之后的最近时段；禁用时返回零值。
// 调用前必须 ApplyDefaults 并 Validate。
func (p NoAuthPeriod) NextWindow(now time.Time) (start, end time.Time) {
	if !p.Enabled {
		return time.Time{}, time.Time{}
	}
	local := now.In(beijing)
	// 时间格式已校验；按位取分钟，避免每轮重新解析时间字符串。
	startMinute := int(p.Start[0]-'0')*600 + int(p.Start[1]-'0')*60 + int(p.Start[3]-'0')*10 + int(p.Start[4]-'0')
	endMinute := int(p.End[0]-'0')*600 + int(p.End[1]-'0')*60 + int(p.End[3]-'0')*10 + int(p.End[4]-'0')
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, beijing)
	// 昨日覆盖跨日尾段；+7 覆盖今天的时段结束后等待整整一周的情形。
	for offset := -1; offset <= 7; offset++ {
		day := midnight.AddDate(0, 0, offset)
		for _, weekday := range p.Weekdays {
			if int(day.Weekday()) != weekday {
				continue
			}
			start = day.Add(time.Duration(startMinute) * time.Minute)
			end = day.Add(time.Duration(endMinute) * time.Minute)
			if endMinute < startMinute {
				end = end.AddDate(0, 0, 1)
			}
			if now.Before(end) {
				return start, end
			}
			break
		}
	}
	return time.Time{}, time.Time{}
}
