package proxy

import "testing"

func TestUpstreamRequestWantsStream(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{name: "missing defaults to true", body: []byte(`{"model":"gpt-5.4"}`), want: true},
		{name: "explicit true", body: []byte(`{"stream":true}`), want: true},
		{name: "explicit false", body: []byte(`{"stream":false}`), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamRequestWantsStream(tc.body); got != tc.want {
				t.Fatalf("upstreamRequestWantsStream(%s) = %v, want %v", string(tc.body), got, tc.want)
			}
		})
	}
}
