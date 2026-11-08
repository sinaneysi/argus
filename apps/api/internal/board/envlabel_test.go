package board

import "testing"

func TestEnvFromLabels(t *testing.T) {
	cases := []struct {
		labels []string
		want   string
	}{
		{[]string{"cicd", "env:stage"}, "develop"},
		{[]string{"env::pre-release"}, "pre-release"},
		{[]string{"env-develop"}, "develop"},
		{[]string{"Env:Prod"}, "release"},
		{[]string{"env-pre"}, "pre-release"},
		{[]string{"env:unknown", "env-dev"}, "develop"},
		{[]string{"status:pending", "team:x"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := envFromLabels(c.labels); got != c.want {
			t.Errorf("envFromLabels(%v) = %q, want %q", c.labels, got, c.want)
		}
	}
}
