#!/usr/bin/env python3
"""Regenerate the conformance fixtures in this directory.

The fixtures are committed; this script exists so the hex encodings are derived
rather than hand-written, and so adding a case is a code edit rather than a
manual hex-assembly exercise.

Stdlib only — run it with any python3:
    python3 testdata/conformance/gen_fixtures.py

(The scaffold's version imported eth_abi to build the ABI-encoded SAY_GOODBYE
payload. USDX_ORACLE has no ABI-encoded instruction payload, so the dependency
is gone. If a future op takes one, reintroduce eth-abi in a venv rather than
hand-assembling tuples.)

Each fixture is a single request/response pair asserted against the extension by
scripts/test-conformance.sh. See docs/extension-contract.md.

The fixtures are ORDER-DEPENDENT and share one process: counters accumulate and
the final fixture asserts the resulting state. index.json fixes the order.
"""

from __future__ import annotations

import json
import pathlib

HERE = pathlib.Path(__file__).parent

ACTION_ID = "0x" + "11" * 32
TEE_ID = "0x" + "22" * 20
VERSION = "0.1.0"

OP_TYPE = "USDX_ORACLE"
OP_COMMAND = "GET_PRICE"

# The instruction timestamp. The scaffold handler echoes this as observedAt,
# which is what keeps these fixtures byte-reproducible. When the real handler
# stamps observedAt from the observation window instead, the exact-match
# assertion on `data` below has to become a structural one.
TIMESTAMP = 1700000000

# $1.00 at config.PriceDecimals (8).
STUB_PRICE = "100000000"
PRICE_DECIMALS = 8


def b32(s: str) -> str:
    b = s.encode("utf-8")
    return "0x" + b.ljust(32, b"\x00").hex()


def to_hex(b: bytes) -> str:
    return "0x" + b.hex()


def action(op_type: str, op_command: str, original: bytes, action_id: str = ACTION_ID) -> dict:
    """Build a POST /action body in the exact shape tee-node sends."""
    data_fixed = {
        "instructionId": action_id,
        "teeId": TEE_ID,
        "timestamp": TIMESTAMP,
        "rewardEpochId": 42,
        "opType": b32(op_type),
        "opCommand": b32(op_command),
        "cosigners": [],
        "cosignersThreshold": 0,
        "originalMessage": to_hex(original),
        "additionalFixedMessage": "0x",
    }
    return {
        "data": {
            "id": action_id,
            "type": "instruction",
            "submissionTag": "submit",
            "message": to_hex(json.dumps(data_fixed).encode("utf-8")),
        },
        "additionalVariableMessages": [],
        "timestamps": [],
        "additionalActionData": "0x",
        "signatures": [],
    }


def get_price(original: bytes = b"") -> dict:
    """A GET_PRICE action. requestPriceUpdate() sends an empty message."""
    return action(OP_TYPE, OP_COMMAND, original)


def json_msg(obj) -> bytes:
    """Serialize compactly, with no whitespace.

    This matters for expected ActionResult.data: the comparison is byte-exact,
    because tee-node hashes `data` (ActionResult.Hash computes keccak256 over
    it) and signs the result. Go's encoding/json emits compact output with
    struct-declaration field order, so key order here must match the field order
    in pkg/types.GetPriceResponse.
    """
    return json.dumps(obj, separators=(",", ":")).encode("utf-8")


PRICE_PAYLOAD = json_msg({
    "price": STUB_PRICE,
    "decimals": PRICE_DECIMALS,
    "observedAt": TIMESTAMP,
})


FIXTURES: list[dict] = [
    {
        "name": "01-get-price-success",
        "description": "GET_PRICE with an empty payload returns the price, decimals and observedAt",
        "request": {"method": "POST", "path": "/action", "body": get_price()},
        "expect": {
            "status": 200,
            "json": {
                "id": ACTION_ID,
                "submissionTag": "submit",
                "status": 1,
                "log": "ok",
                "opType": b32(OP_TYPE),
                "opCommand": b32(OP_COMMAND),
                # hexutil.Bytes with no omitempty marshals as "0x", never absent.
                "additionalResultStatus": "0x",
                # Plain string, NOT bytes32 — contract §4.4.
                "version": VERSION,
                "data": to_hex(PRICE_PAYLOAD),
            },
        },
    },
    {
        "name": "02-get-price-is-deterministic",
        "description": (
            "A second GET_PRICE returns an identical payload. Unlike the scaffold's "
            "greeting counter, the price is a function of observed inputs, not of how "
            "many times it has been asked — nothing in the payload may drift per call."
        ),
        "request": {"method": "POST", "path": "/action", "body": get_price()},
        "expect": {
            "status": 200,
            "json_subset": {"status": 1, "data": to_hex(PRICE_PAYLOAD)},
        },
    },
    {
        "name": "03-get-price-rejects-payload",
        "description": (
            "requestPriceUpdate() sends no payload. A non-empty message is a handler "
            "error, not silently ignored: HTTP 200, status 0, no data."
        ),
        "request": {"method": "POST", "path": "/action",
                    "body": get_price(json_msg({"unexpected": "payload"}))},
        "expect": {
            "status": 200,
            # data is still emitted, as "0x" — the Go struct has no omitempty.
            "json_subset": {"status": 0, "data": "0x"},
            "log_prefix": "error: ",
        },
    },
    {
        "name": "04-unknown-op-type",
        "description": "An unrouted opType is 501 with a plain-text body",
        "request": {"method": "POST", "path": "/action",
                    "body": action("NOT_A_REAL_TYPE", OP_COMMAND, b"")},
        # Go names the received vs expected opType, which is more useful than a
        # bare string. The status code is contractual; the wording is not.
        "expect": {"status": 501, "text_contains": "unsupported op type"},
    },
    {
        "name": "05-unknown-op-command",
        "description": "A known opType with an unrouted opCommand is also 501",
        "request": {"method": "POST", "path": "/action",
                    "body": action(OP_TYPE, "NOT_A_COMMAND", b"")},
        "expect": {"status": 501, "text_contains": "unsupported op command"},
    },
    {
        "name": "06-invalid-action-json",
        "description": "A body that is not JSON is 400",
        "request": {"method": "POST", "path": "/action", "raw_body": "not json at all"},
        "expect": {"status": 400},
    },
    {
        "name": "07-invalid-hex-in-message",
        "description": "A non-hex data.message is 400",
        "request": {
            "method": "POST", "path": "/action",
            "body": {"data": {"id": ACTION_ID, "type": "instruction",
                              "submissionTag": "submit", "message": "0xZZZZ"}},
        },
        "expect": {"status": 400},
    },
    {
        "name": "08-message-not-datafixed",
        "description": "A valid-hex message that is not DataFixed JSON is 400",
        "request": {
            "method": "POST", "path": "/action",
            "body": {"data": {"id": ACTION_ID, "type": "instruction",
                              "submissionTag": "submit", "message": to_hex(b"not json")}},
        },
        "expect": {"status": 400},
    },
    {
        "name": "09-get-action-not-allowed",
        "description": "GET /action is 405",
        "request": {"method": "GET", "path": "/action"},
        "expect": {"status": 405},
    },
    {
        "name": "10-post-state-not-allowed",
        "description": "POST /state is 405",
        "request": {"method": "POST", "path": "/state", "raw_body": ""},
        "expect": {"status": 405},
    },
    {
        "name": "11-unknown-path",
        "description": "An unknown path is 404",
        "request": {"method": "GET", "path": "/does-not-exist"},
        "expect": {"status": 404},
    },
    {
        "name": "12-get-state",
        "description": (
            "GET /state returns bytes32 stateVersion and operational state only. "
            "Two successes and one rejected payload have accumulated by now, and the "
            "error class from fixture 03 is still the last one recorded. "
            "This is a whole-body exact match on purpose: spec §4.1 makes /state "
            "public and attested, so an accidentally added field carrying a supply or "
            "reserve figure must fail the suite rather than slip through."
        ),
        "request": {"method": "GET", "path": "/state"},
        "expect": {
            "status": 200,
            "json": {
                # Asymmetric with ActionResult.version by design — contract §4.5.
                "stateVersion": b32(VERSION),
                "state": {
                    # Stub mode has no provisioner, so provisioning has not
                    # happened and every flag is false. In production this is
                    # what distinguishes "never configured" from "configured but
                    # a source is down" — see the findings doc §14.
                    "readiness": {
                        "endpointsSet": False,
                        "secretsSet": False,
                        "ready": False,
                        "missingSecretRefs": [],
                    },
                    "lastPrice": STUB_PRICE,
                    "lastDecimals": PRICE_DECIMALS,
                    "lastObservedAt": TIMESTAMP,
                    "updateCount": 2,
                    "failureCount": 1,
                    "lastErrorClass": "source_invalid",
                    "sourceHealth": {},
                },
            },
        },
    },
]


def main() -> None:
    index = []
    for f in FIXTURES:
        path = HERE / f"{f['name']}.json"
        path.write_text(json.dumps(f, indent=2) + "\n")
        index.append(f["name"])

    (HERE / "index.json").write_text(json.dumps({"fixtures": index}, indent=2) + "\n")

    # Drop fixtures that are no longer generated. Without this, renaming a case
    # leaves the old file behind: harmless to the suite, which reads index.json,
    # but it reads as a live fixture to anyone browsing the directory.
    keep = {f"{name}.json" for name in index} | {"index.json"}
    removed = []
    for path in sorted(HERE.glob("*.json")):
        if path.name not in keep:
            path.unlink()
            removed.append(path.name)

    print(f"wrote {len(index)} fixtures + index.json to {HERE}")
    if removed:
        print(f"removed {len(removed)} stale fixture(s): {', '.join(removed)}")


if __name__ == "__main__":
    main()
