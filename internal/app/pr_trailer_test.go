package app

import (
	"strings"
	"testing"
)

// The PR trailer is an instruction in SessionStart context, the way Claude
// Code adds its own footer: no enforcement, no command rewriting. These
// tests pin where it appears and that hook and skill carry one text.

const wantPRTrailer = "Shipped from a [flow](https://github.com/Facets-cloud/flow) task."

func TestPRTrailerTextIsVerbatim(t *testing.T) {
	if prTrailer != wantPRTrailer {
		t.Fatalf("prTrailer = %q, want %q", prTrailer, wantPRTrailer)
	}
	for _, bad := range []string{"%", "{{", "<slug>", "session"} {
		if strings.Contains(prTrailer, bad) {
			t.Errorf("trailer must be a fixed line with nothing substituted, found %q", bad)
		}
	}
}

func TestSessionStartBoundTabGetsPRTrailer(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	t.Setenv(prTrailerEnvVar, "")
	if rc := cmdAdd([]string{"task", "Tabby"}); rc != 0 {
		t.Fatal("add task")
	}
	bindSession(t, "tabby", "sid-tabby")
	out := sessionStartOutput(t)
	if !strings.Contains(out, prTrailer) {
		t.Fatalf("bound tab session should be told the PR trailer, got:\n%s", out)
	}
	if !strings.Contains(out, prTrailerEnvVar+"=off") {
		t.Errorf("trailer instruction should name the off switch %s=off", prTrailerEnvVar)
	}
	// The instruction must not smuggle identifiers into the trailer line.
	line := prTrailerBlock()
	for _, bad := range []string{"tabby", "sid-tabby"} {
		if strings.Contains(line, bad) {
			t.Errorf("trailer instruction must not carry %q:\n%s", bad, line)
		}
	}
}

func TestSessionStartAutoChildGetsPRTrailer(t *testing.T) {
	t.Run("running auto row", func(t *testing.T) {
		setupFlowRoot(t)
		t.Setenv("FLOW_AUTO_RUN", "")
		t.Setenv(prTrailerEnvVar, "")
		if rc := cmdAdd([]string{"task", "Child"}); rc != 0 {
			t.Fatal("add task")
		}
		seedRunningAuto(t, "child", 999999)
		t.Setenv("CLAUDE_CODE_SESSION_ID", fakeSessionID("child"))
		if out := sessionStartOutput(t); !strings.Contains(out, prTrailer) {
			t.Errorf("headless child should be told the PR trailer:\n%s", out)
		}
	})
	t.Run("FLOW_AUTO_RUN env", func(t *testing.T) {
		setupFlowRoot(t)
		t.Setenv(prTrailerEnvVar, "")
		if rc := cmdAdd([]string{"task", "Child"}); rc != 0 {
			t.Fatal("add task")
		}
		bindSession(t, "child", "sid-child")
		t.Setenv("FLOW_AUTO_RUN", "child")
		if out := sessionStartOutput(t); !strings.Contains(out, prTrailer) {
			t.Errorf("FLOW_AUTO_RUN session should be told the PR trailer:\n%s", out)
		}
	})
}

func TestSessionStartUnboundGetsNoPRTrailer(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	t.Setenv(prTrailerEnvVar, "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	if out := sessionStartOutput(t); strings.Contains(out, prTrailer) {
		t.Errorf("unbound session must not be told the PR trailer:\n%s", out)
	}
}

func TestSessionStartPRTrailerOffSwitch(t *testing.T) {
	for _, off := range []string{"off", "0", "false", "OFF", "False"} {
		t.Run(off, func(t *testing.T) {
			setupFlowRoot(t)
			t.Setenv("FLOW_AUTO_RUN", "")
			if rc := cmdAdd([]string{"task", "Quiet"}); rc != 0 {
				t.Fatal("add task")
			}
			bindSession(t, "quiet", "sid-quiet")
			t.Setenv(prTrailerEnvVar, off)
			out := sessionStartOutput(t)
			if strings.Contains(out, prTrailer) || strings.Contains(out, "trailer") {
				t.Errorf("%s=%s must drop the trailer instruction:\n%s", prTrailerEnvVar, off, out)
			}
			if !strings.Contains(out, "flow execution session") {
				t.Errorf("the rest of the bound context must still be emitted:\n%s", out)
			}
		})
	}
	// Any other value (including "on") keeps it.
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	if rc := cmdAdd([]string{"task", "Loud"}); rc != 0 {
		t.Fatal("add task")
	}
	bindSession(t, "loud", "sid-loud")
	t.Setenv(prTrailerEnvVar, "on")
	if out := sessionStartOutput(t); !strings.Contains(out, prTrailer) {
		t.Errorf("%s=on must keep the trailer:\n%s", prTrailerEnvVar, out)
	}
}

// TestSkillPinsPRTrailer keeps hook and docs on one text: the skill must
// quote the Go constant verbatim and name the off switch.
func TestSkillPinsPRTrailer(t *testing.T) {
	corpus := skillCorpus(t)
	if !strings.Contains(corpus, prTrailer) {
		t.Errorf("skill corpus must quote the PR trailer verbatim: %q", prTrailer)
	}
	if !strings.Contains(corpus, prTrailerEnvVar+"=off") {
		t.Errorf("skill corpus must name the off switch %s=off", prTrailerEnvVar)
	}
	if strings.Count(corpus, prTrailer) != 1 {
		t.Errorf("trailer text should live in exactly one place in the skill, found %d", strings.Count(corpus, prTrailer))
	}
}
