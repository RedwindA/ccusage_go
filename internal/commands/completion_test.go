package commands

import (
	"strings"
	"testing"
)

func TestCompletionScriptTargetsInstalledBinary(t *testing.T) {
	out, err := executeReport("completion", "bash")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "complete -o default -F __start_ccusage_go ccusage_go") {
		t.Fatalf("bash completion is not registered for ccusage_go:\n%s", out)
	}
}

func TestFlagValueCompletion(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"weekly", "--format", ""}, []string{"table", "json", "csv"}},
		{[]string{"claude", "daily", "--order", ""}, []string{"asc", "desc"}},
		{[]string{"--sections", "daily,"}, []string{"daily,weekly", "daily,monthly", "daily,session"}},
		{[]string{"claude", "statusline", "--cost-source", ""}, []string{"auto", "ccusage", "cc", "both"}},
		{[]string{"blocks", "--token-limit", ""}, []string{"max"}},
	} {
		out, err := executeReport(append([]string{"__complete"}, tc.args...)...)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(out, "\n")
		if got := lines[:len(tc.want)]; strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%v: got %q, want %q", tc.args, got, tc.want)
		}
	}
}
