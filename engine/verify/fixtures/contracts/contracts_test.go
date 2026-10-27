package contracts_test

import (
	"testing"

	"example.com/project/engine/base/contract"
	"example.com/project/engine/verify/fixtures/contracts"
)

func TestAllContractFilesLoadAndSelfCheck(t *testing.T) {
	set, err := contract.Load(contracts.FS)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"chat.v1.message.seen", "identity.v1.chat.get", "identity.v1.user.get", "profile.v1.get", "stats.v1.messages.count"}
	got := set.Names()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
