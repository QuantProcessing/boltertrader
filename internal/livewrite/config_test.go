package livewrite

import "testing"

func TestEnabledUsesOneStrictDefaultOnBothProcessBoundaries(t *testing.T) {
	for _, test := range []struct {
		value   string
		enabled bool
		wantErr bool
	}{
		{enabled: true},
		{value: " true ", enabled: true},
		{value: "FALSE", enabled: false},
		{value: "yes", wantErr: true},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv(EnvName, test.value)
			enabled, err := Enabled()
			if (err != nil) != test.wantErr || enabled != test.enabled {
				t.Fatalf("Enabled() = %v, %v", enabled, err)
			}
		})
	}
}
