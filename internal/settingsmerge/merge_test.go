package settingsmerge

import (
	"encoding/json"
	"reflect"
	"testing"
)

func obj(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func hook(cmd string) string {
	return `{"hooks":[{"type":"command","command":"` + cmd + `"}]}`
}

func TestNewProductHookLandsAndCustomerHookSurvives(t *testing.T) {
	oldP := obj(t, `{"hooks":{"Stop":[`+hook("a")+`]}}`)
	newP := obj(t, `{"hooks":{"Stop":[`+hook("a")+`,`+hook("b")+`]}}`)
	cust := obj(t, `{"hooks":{"Stop":[`+hook("a")+`,`+hook("mine")+`]}}`)
	got := Merge(oldP, newP, cust, nil)
	want := obj(t, `{"hooks":{"Stop":[`+hook("a")+`,`+hook("mine")+`,`+hook("b")+`]}}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestHookDroppedByProductIsRemovedButCustomerCopyOfChangedOneIsKept(t *testing.T) {
	oldP := obj(t, `{"hooks":{"Stop":[`+hook("gone")+`,`+hook("keep")+`]}}`)
	newP := obj(t, `{"hooks":{"Stop":[`+hook("keep")+`]}}`)
	cust := obj(t, `{"hooks":{"Stop":[`+hook("gone")+`,`+hook("keep")+`]}}`)
	got := Merge(oldP, newP, cust, nil)
	want := obj(t, `{"hooks":{"Stop":[`+hook("keep")+`]}}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestScalarsFollowProductUnlessCustomerChangedThem(t *testing.T) {
	oldP := obj(t, `{"effortLevel":"high","theme":"dark","skipDangerousModePermissionPrompt":true}`)
	newP := obj(t, `{"effortLevel":"medium","theme":"light"}`)
	cust := obj(t, `{"effortLevel":"high","theme":"solarized","skipDangerousModePermissionPrompt":true}`)
	got := Merge(oldP, newP, cust, nil)
	want := obj(t, `{"effortLevel":"medium","theme":"solarized"}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestFirstInstallKeepsCustomerValuesAndAddsProductOnes(t *testing.T) {
	newP := obj(t, `{"effortLevel":"medium","hooks":{"Stop":[`+hook("p")+`]},"env":{"A":"1"}}`)
	cust := obj(t, `{"effortLevel":"high","hooks":{"Stop":[`+hook("c")+`]},"env":{"B":"2"}}`)
	got := Merge(nil, newP, cust, nil)
	want := obj(t, `{"effortLevel":"high","hooks":{"Stop":[`+hook("c")+`,`+hook("p")+`]},"env":{"A":"1","B":"2"}}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestNormalizeStopsPython3RewriteFromDuplicatingHooks(t *testing.T) {
	oldP := obj(t, `{"hooks":{"Stop":[`+hook(`python x.py`)+`]}}`)
	newP := obj(t, `{"hooks":{"Stop":[`+hook(`python x.py`)+`,`+hook(`python y.py`)+`]}}`)
	cust := obj(t, `{"hooks":{"Stop":[`+hook(`python3 x.py`)+`]}}`)
	norm := func(cmd string) string {
		if len(cmd) > 7 && cmd[:7] == "python " {
			return "python3 " + cmd[7:]
		}
		return cmd
	}
	got := Merge(oldP, newP, cust, norm)
	want := obj(t, `{"hooks":{"Stop":[`+hook(`python3 x.py`)+`,`+hook(`python3 y.py`)+`]}}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestInputsAreNotMutated(t *testing.T) {
	oldP := obj(t, `{"a":{"b":1}}`)
	newP := obj(t, `{"a":{"b":2}}`)
	cust := obj(t, `{"a":{"b":1}}`)
	before := obj(t, `{"a":{"b":1}}`)
	Merge(oldP, newP, cust, nil)
	if !reflect.DeepEqual(cust, before) {
		t.Fatal("customer settings mutated")
	}
}
