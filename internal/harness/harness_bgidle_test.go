package harness

import "testing"

func TestBackgroundAgentProvablyIdle(t *testing.T) {
	quiet := &BackgroundActivity{}
	cases := []struct {
		name string
		a    BackgroundAgent
		want bool
	}{
		{"idle and quiet", BackgroundAgent{PID: 1, Status: "idle", State: "working", Activity: quiet}, true},
		{"idle done and quiet", BackgroundAgent{PID: 1, Status: "idle", State: "done", Activity: quiet}, true},
		{"busy", BackgroundAgent{PID: 1, Status: "busy", State: "working", Activity: quiet}, false},
		{"blocked on the user", BackgroundAgent{PID: 1, Status: "idle", State: "blocked", Activity: quiet}, false},
		{"needs input", BackgroundAgent{PID: 1, Status: "idle", State: "working", Activity: &BackgroundActivity{NeedsInput: true}}, false},
		{"work in flight", BackgroundAgent{PID: 1, Status: "idle", State: "working", Activity: &BackgroundActivity{InFlight: 1}}, false},
		{"activity unknown", BackgroundAgent{PID: 1, Status: "idle", State: "working"}, false},
		{"not live", BackgroundAgent{Status: "idle", State: "working", Activity: quiet}, false},
	}
	for _, tc := range cases {
		ok, why := tc.a.ProvablyIdle()
		if ok != tc.want {
			t.Errorf("%s: ProvablyIdle = %v (%q), want %v", tc.name, ok, why, tc.want)
		}
		if !ok && why == "" {
			t.Errorf("%s: no reason given", tc.name)
		}
	}
}
