package question

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestInventoryCanonicalBoundedAndDetached(t *testing.T) {
	d, _ := Seal(Fixture())
	other := Fixture()
	other.HorizonSeconds++
	e, _ := Seal(other)
	receiverID, hash := "receiver-abcdefghijklmnop", news.SHA256("catalogue")
	i, err := MakeInventory(receiverID, hash, []Result{d, e, d})
	if err != nil || i.Validate() != nil || len(i.Refs()) != 2 {
		t.Fatal(i, err)
	}
	again, err := MakeInventory(receiverID, hash, []Result{e, d})
	if err != nil || !reflect.DeepEqual(i, again) {
		t.Fatal("order or identical publications changed inventory", err)
	}
	d.Declaration.Methods[0].SHA256 = "mutated"
	if i.Validate() != nil {
		t.Fatal("caller mutated inventory")
	}
	empty, err := MakeInventory(receiverID, hash, nil)
	if err != nil || empty.Validate() != nil || empty.Declarations == nil || empty.Refs() == nil {
		t.Fatal(empty, err)
	}
	all := []Result{}
	for n := range MaxDeclarations {
		declaration := Fixture()
		declaration.Label = fmt.Sprintf("label-%d", n)
		sealed, _ := Seal(declaration)
		all = append(all, sealed)
	}
	full, err := MakeInventory(receiverID, hash, all)
	if err != nil || full.Validate() != nil || len(full.Refs()) != 32 {
		t.Fatal(full, err)
	}
	if _, err = MakeInventory(receiverID, hash, append(all, all[0])); err == nil {
		t.Fatal("input overflow silently deduplicated")
	}
	for _, mutate := range []func(*Inventory){
		func(i *Inventory) { i.ReceiverID = "wrong" },
		func(i *Inventory) { i.CatalogueSHA256 = "wrong" },
		func(i *Inventory) { i.Declarations[0].Declaration.HorizonSeconds++ },
		func(i *Inventory) { i.Declarations[0].Methods[0].Strategy.Quantity++ },
		func(i *Inventory) { i.Declarations = append(i.Declarations, i.Declarations[0]) },
		func(i *Inventory) { i.Declarations[0], i.Declarations[1] = i.Declarations[1], i.Declarations[0] },
	} {
		raw, _ := json.Marshal(i)
		var bad Inventory
		_ = json.Unmarshal(raw, &bad)
		mutate(&bad)
		bad.SHA256 = ""
		raw, _ = json.Marshal(bad)
		bad.SHA256 = news.SHA256(string(raw))
		if bad.Validate() == nil {
			t.Fatal("resealed invalid inventory accepted")
		}
	}
}
