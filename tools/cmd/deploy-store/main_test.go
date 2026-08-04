package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"fce-usdx-oracle/tools/pkg/support"
)

const sampleAddresses = `[
  {
    "name": "FlareTeeManager",
    "contractName": "FlareTeeManager.sol",
    "address": "0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE"
  }
]`

func writeTempAddresses(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deployed-addresses.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp addresses file: %v", err)
	}
	return path
}

func readAddresses(t *testing.T, path string) []support.RawContract {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read addresses file: %v", err)
	}
	var entries []support.RawContract
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("addresses file is no longer valid JSON: %v", err)
	}
	return entries
}

func TestRecordAddresses_AppendsNewEntries(t *testing.T) {
	path := writeTempAddresses(t, sampleAddresses)

	err := recordAddresses(path, []support.RawContract{
		{Name: "UsdxPriceStore", ContractName: "UsdxPriceStore.sol", Address: "0x0000000000000000000000000000000000000001"},
		{Name: "UsdxCustomFeed", ContractName: "UsdxCustomFeed.sol", Address: "0x0000000000000000000000000000000000000002"},
	})
	if err != nil {
		t.Fatalf("recordAddresses: %v", err)
	}

	entries := readAddresses(t, path)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Name != "FlareTeeManager" ||
		entries[0].Address != "0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE" {
		t.Errorf("existing entry was clobbered: %+v", entries[0])
	}
	if entries[1].Name != "UsdxPriceStore" || entries[1].Address != "0x0000000000000000000000000000000000000001" {
		t.Errorf("store entry missing or wrong: %+v", entries[1])
	}
	if entries[2].Name != "UsdxCustomFeed" || entries[2].Address != "0x0000000000000000000000000000000000000002" {
		t.Errorf("feed entry missing or wrong: %+v", entries[2])
	}
}

func TestRecordAddresses_UpdatesExistingEntryInPlace(t *testing.T) {
	path := writeTempAddresses(t, sampleAddresses)

	first := []support.RawContract{
		{Name: "UsdxPriceStore", ContractName: "UsdxPriceStore.sol", Address: "0x0000000000000000000000000000000000000001"},
	}
	if err := recordAddresses(path, first); err != nil {
		t.Fatalf("first recordAddresses: %v", err)
	}

	second := []support.RawContract{
		{Name: "UsdxPriceStore", ContractName: "UsdxPriceStore.sol", Address: "0x0000000000000000000000000000000000000009"},
	}
	if err := recordAddresses(path, second); err != nil {
		t.Fatalf("second recordAddresses: %v", err)
	}

	entries := readAddresses(t, path)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after re-record, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Name == "UsdxPriceStore" && e.Address != "0x0000000000000000000000000000000000000009" {
			t.Errorf("store entry not updated in place: %+v", e)
		}
	}
}
