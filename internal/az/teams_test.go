package az

import "testing"

func TestTeamReadRejectsInvalidScopeBeforeRequest(t *testing.T) {
	for _, tc := range []struct {
		command string
		opts    map[string]string
	}{
		{"devops team list", map[string]string{"detect": "false"}},
		{"devops team list", map[string]string{"project": "a%252fb"}},
		{"devops team list-member", map[string]string{}},
		{"devops team list-member", map[string]string{"team": ".."}},
		{"devops team list-member", map[string]string{"team": "Team", "top": "-1"}},
	} {
		if _, err := parseTeamRead(tc.command, tc.opts); err == nil {
			t.Errorf("parseTeamRead(%s, %v) accepted invalid input", tc.command, tc.opts)
		}
	}
}
