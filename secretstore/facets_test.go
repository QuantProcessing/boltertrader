package secretstore_test

import (
	"reflect"
	"strings"
	"testing"
)

func TestFilesystemFacetsExposeOnlyTheirContractMethods(t *testing.T) {
	provider := newTestProvider(t)
	methods := func(value any) []string {
		typeOf := reflect.TypeOf(value)
		result := make([]string, typeOf.NumMethod())
		for index := range result {
			result[index] = typeOf.Method(index).Name
		}
		return result
	}
	if got := strings.Join(methods(provider.Writer()), ","); got != "Check,Delete,LookupOperation,Put" {
		t.Fatalf("writer methods=%s", got)
	}
	if got := strings.Join(methods(provider.Resolver()), ","); got != "Check,Resolve" {
		t.Fatalf("resolver methods=%s", got)
	}
}
