package tincancli

import (
	"reflect"
	"testing"

	"github.com/roman/tincan/pkg/herdr"
)

func TestDoorbellAdapterExcludesWorkerLifecycle(t *testing.T) {
	adapter := reflect.TypeOf(herdr.Client{})
	var methods []string
	for index := 0; index < adapter.NumMethod(); index++ {
		methods = append(methods, adapter.Method(index).Name)
	}
	if !reflect.DeepEqual(methods, []string{"ForSession", "Prompt"}) {
		t.Fatalf("transport doorbell methods = %v, want only ForSession and Prompt", methods)
	}
}
