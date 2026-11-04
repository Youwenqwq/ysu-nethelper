package eportal

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const (
	deviceOne  = "00000000000000000000000000000001"
	deviceTwo  = "abcdefabcdefabcdefabcdefabcdefab"
	deviceData = `{"onlineDevices":[
		{"onlineUserUuid":"` + deviceOne + `","deviceName":"电脑","userIpv4":"10.0.0.1","userMac":"AA-BB-CC-DD-EE-01","currentDevice":true},
		{"onlineUserUuid":"` + deviceTwo + `","deviceName":"手机","userIpv4":"10.0.0.2","deviceType":"手机","onlineDuration":"1小时"}
	],"offlineDevices":[]}`
)

func devicesStep(data string) portalStep {
	return portalStep{method: "POST", path: "/eportal/adaptor/devices/findDevice",
		payload: `{"sessionId":"before"}`, data: envelopeData(data)}
}

func TestListDevicesUsesOnlineSession(t *testing.T) {
	steps := append(sessionSteps("before", "中国联通", true), devicesStep(deviceData))
	list, err := newScriptedClient(t, steps).ListDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Online) != 2 || list.Online[0].UUID != deviceOne || !list.Online[0].Current ||
		list.Online[1].Current || list.Online[1].OnlineDuration != "1小时" || list.Raw["offlineDevices"] == nil {
		t.Fatalf("incorrect list: %+v", list)
	}
}

func TestListDevicesEmptyAndMalformed(t *testing.T) {
	for _, tt := range []struct {
		data  string
		valid bool
	}{
		{`{"onlineDevices":null}`, true},
		{`{"onlineDevices":[]}`, true},
		{`{}`, false},
		{`null`, false},
		{`{"onlineDevices":{}}`, false},
		{`{"onlineDevices":[{}]}`, false},
		{`{"onlineDevices":[{"onlineUserUuid":"` + deviceOne + `"},{"onlineUserUuid":"` + deviceOne + `"}]}`, false},
	} {
		t.Run(tt.data, func(t *testing.T) {
			steps := append(sessionSteps("before", "中国联通", true), devicesStep(tt.data))
			list, err := newScriptedClient(t, steps).ListDevices(context.Background())
			if tt.valid {
				if err != nil || list.Online == nil || len(list.Online) != 0 {
					t.Fatalf("expected an empty array: %+v, %v", list, err)
				}
			} else if !errors.Is(err, ErrProtocol) {
				t.Fatalf("expected protocol error: %v", err)
			}
		})
	}
}

func TestOfflineDeviceCommandsNeverFallBackToCAS(t *testing.T) {
	for _, kick := range []bool{false, true} {
		client := newScriptedClient(t, sessionSteps("before", "", false))
		var err error
		if kick {
			_, err = client.KickDevices(context.Background(), []string{"1"})
		} else {
			_, err = client.ListDevices(context.Background())
		}
		if !errors.Is(err, ErrBusiness) {
			t.Fatalf("expected offline error, got %v", err)
		}
	}
}

func TestKickDevicesUsesSameSessionAndDeduplicates(t *testing.T) {
	steps := append(sessionSteps("before", "中国联通", true), devicesStep(deviceData))
	steps = append(steps, portalStep{method: "POST", path: "/eportal/adaptor/kick-offline/batch",
		payload: `{"sessionId":"before","onlineUserUuids":["` + deviceTwo + `","` + deviceOne + `"]}`,
		data:    `{"code":200,"message":"OK","success":true,"data":null}`})
	selected, err := newScriptedClient(t, steps).KickDevices(context.Background(), []string{"2", deviceOne, "1", "ABCDEFABCDEFABCDEFABCDEFABCDEFAB"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].UUID != deviceTwo || selected[1].UUID != deviceOne {
		t.Fatalf("wrong selected devices: %+v", selected)
	}
}

func TestKickDevicesInvalidTargetPreventsAllMutations(t *testing.T) {
	for _, targets := range [][]string{{"1", "3"}, {"0"}, {"-1"}, {"00000000000000000000000000000009"}, {"all"}, {""}} {
		steps := append(sessionSteps("before", "中国联通", true), devicesStep(deviceData))
		if _, err := newScriptedClient(t, steps).KickDevices(context.Background(), targets); err == nil {
			t.Fatalf("accepted invalid targets: %v", targets)
		}
	}
	if _, err := newScriptedClient(t, nil).KickDevices(context.Background(), nil); err == nil {
		t.Fatal("accepted an empty request")
	}
}

func TestKickDevicesRequiresExplicitAcknowledgement(t *testing.T) {
	for _, data := range []string{
		`{"code":403,"message":"会话失效"}`,
		`{"code":200,"message":"OK","success":false}`,
		`{"code":200,"message":"设备下线失败"}`,
		`{"code":200,"data":null}`,
		`<html>login</html>`,
	} {
		steps := append(sessionSteps("before", "中国联通", true), devicesStep(deviceData))
		steps = append(steps, portalStep{method: "POST", path: "/eportal/adaptor/kick-offline/batch",
			payload: `{"sessionId":"before","onlineUserUuids":["` + deviceTwo + `"]}`, data: data})
		if _, err := newScriptedClient(t, steps).KickDevices(context.Background(), []string{"2"}); err == nil {
			t.Fatalf("reported success for rejected request: %s", data)
		}
	}
}

func TestDeviceUUIDTakesPrecedenceOverNumericIndex(t *testing.T) {
	devices := []Device{{UUID: deviceTwo}, {UUID: deviceOne}}
	selected, err := resolveDeviceTargets([]string{deviceOne}, devices)
	if err != nil || !reflect.DeepEqual(selected, []Device{devices[1]}) {
		t.Fatalf("numeric UUID misinterpreted: %v, %v", selected, err)
	}
}
