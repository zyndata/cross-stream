package main

import (
	"testing"

	"github.com/zyndata/cross-stream/internal/hub"
)

func TestParseLoglevel(t *testing.T) {
	tests := []struct {
		args    []string
		want    hub.LogLevelParams
		wantErr bool
	}{
		{nil, hub.LogLevelParams{}, false},
		{[]string{"debug"}, hub.LogLevelParams{Level: "debug"}, false},
		{[]string{"debug", "-for", "15m"}, hub.LogLevelParams{Level: "debug", ForSecs: 900}, false},
		{[]string{"-for", "1m", "debug"}, hub.LogLevelParams{Level: "debug", ForSecs: 60}, false},
		{[]string{"info", "-for", "200ms"}, hub.LogLevelParams{Level: "info", ForSecs: 1}, false},
		{[]string{"-for", "1m"}, hub.LogLevelParams{}, true},
		{[]string{"trace"}, hub.LogLevelParams{}, true},
		{[]string{"debug", "info"}, hub.LogLevelParams{}, true},
		{[]string{"debug", "-for", "-1m"}, hub.LogLevelParams{}, true},
	}
	for _, tc := range tests {
		got, err := parseLoglevel(tc.args)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%q: err %v", tc.args, err)
		}
		if !tc.wantErr && got != tc.want {
			t.Fatalf("%q: got %+v want %+v", tc.args, got, tc.want)
		}
	}
}
