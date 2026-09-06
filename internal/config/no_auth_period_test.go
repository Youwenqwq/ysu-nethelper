package config

import (
	"testing"
	"time"
)

func TestNoAuthPeriodBoundaries(t *testing.T) {
	cfg := Config{}
	cfg.ApplyDefaults()
	p := cfg.Daemon.NoAuthPeriod
	p.Enabled = true
	for _, tt := range []struct {
		name, now, start, end string
	}{
		{"before Sunday", "2026-09-06T23:29:59+08:00", "2026-09-06T23:30:00+08:00", "2026-09-07T05:30:00+08:00"},
		{"inclusive start", "2026-09-06T23:30:00+08:00", "2026-09-06T23:30:00+08:00", "2026-09-07T05:30:00+08:00"},
		{"Monday tail in UTC", "2026-09-06T21:29:59Z", "2026-09-06T23:30:00+08:00", "2026-09-07T05:30:00+08:00"},
		{"exclusive end", "2026-09-07T05:30:00+08:00", "2026-09-07T23:30:00+08:00", "2026-09-08T05:30:00+08:00"},
		{"Friday tail", "2026-09-11T05:29:59+08:00", "2026-09-10T23:30:00+08:00", "2026-09-11T05:30:00+08:00"},
		{"Friday end", "2026-09-11T05:30:00+08:00", "2026-09-13T23:30:00+08:00", "2026-09-14T05:30:00+08:00"},
		{"Saturday morning", "2026-09-12T02:00:00+08:00", "2026-09-13T23:30:00+08:00", "2026-09-14T05:30:00+08:00"},
		{"Sunday morning", "2026-09-13T02:00:00+08:00", "2026-09-13T23:30:00+08:00", "2026-09-14T05:30:00+08:00"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tt.now)
			start, end := p.NextWindow(now)
			if start.Format(time.RFC3339) != tt.start || end.Format(time.RFC3339) != tt.end {
				t.Fatalf("window = [%s, %s), want [%s, %s)", start, end, tt.start, tt.end)
			}
		})
	}
}

func TestNoAuthPeriodCustomDaytimeAndWeekWrap(t *testing.T) {
	p := NoAuthPeriod{Enabled: true, Weekdays: []int{6}, Start: "08:00", End: "09:00"}
	now := time.Date(2026, 9, 12, 8, 30, 0, 0, beijing)
	start, end := p.NextWindow(now)
	if !start.Equal(now.Add(-30*time.Minute)) || !end.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("daytime window = [%s, %s)", start, end)
	}
	nextStart, nextEnd := p.NextWindow(end)
	if !nextStart.Equal(start.AddDate(0, 0, 7)) || !nextEnd.Equal(end.AddDate(0, 0, 7)) {
		t.Fatalf("next weekly window = [%s, %s)", nextStart, nextEnd)
	}
}

func TestNoAuthPeriodOptIn(t *testing.T) {
	cfg := Config{Username: "user", Password: "password"}
	cfg.ApplyDefaults()
	now := time.Date(2026, 9, 7, 1, 0, 0, 0, beijing)
	start, end := cfg.Daemon.NoAuthPeriod.NextWindow(now)
	if !start.IsZero() || !end.IsZero() {
		t.Fatal("omitted policy must not block authentication")
	}
	cfg.Daemon.NoAuthPeriod.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	start, end = cfg.Daemon.NoAuthPeriod.NextWindow(now)
	if now.Before(start) || !now.Before(end) {
		t.Fatal("enabled default policy must cover Monday morning")
	}
}

func TestNoAuthPeriodValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*NoAuthPeriod)
	}{
		{"empty weekdays", func(p *NoAuthPeriod) { p.Weekdays = []int{} }},
		{"negative weekday", func(p *NoAuthPeriod) { p.Weekdays = []int{-1} }},
		{"weekday overflow", func(p *NoAuthPeriod) { p.Weekdays = []int{7} }},
		{"non padded hour", func(p *NoAuthPeriod) { p.Start = "8:00" }},
		{"invalid hour", func(p *NoAuthPeriod) { p.Start = "24:00" }},
		{"invalid minute", func(p *NoAuthPeriod) { p.End = "05:60" }},
		{"ambiguous full day", func(p *NoAuthPeriod) { p.End = p.Start }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Username: "user", Password: "password"}
			cfg.ApplyDefaults()
			cfg.Daemon.NoAuthPeriod.Enabled = true
			tt.change(&cfg.Daemon.NoAuthPeriod)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid enabled policy accepted")
			}
		})
	}
}
