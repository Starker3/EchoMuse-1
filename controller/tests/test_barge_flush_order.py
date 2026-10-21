"""
A barge-in over a reply flushes the speaker BEFORE arbitration.

Every barge during playback stops the reply, whoever wins the claim, and the
claim holds up to 250ms (em_listen.ARB_HOLD_S). Flushing after it left the
reply playing into the barge session's first frames: on VVV, 2026-10-21,
"never mind" over a reply reached HA as " one quince. Never mind.", HA's own
sentence did not match, and the LLM answered it 11s later.
"""

import asyncio

import pytest

import _oww_stub

_oww_stub.install()

import em_controller  # noqa: E402 — must follow the stub
import em_state  # noqa: E402
from _sources import code_only, def_source  # noqa: E402


@pytest.fixture(autouse=True, scope="module")
def _drop_the_stub():
    yield
    _oww_stub.uninstall()


def _barging_device(events, monkeypatch):
    d = em_controller.Device("dev1", "10.0.0.1", [], None)
    d.speaking = True
    d.barge_in_enabled = True
    d.wake_arb_ms = 700

    async def send_control(msg):
        events.append(msg["type"])

    async def nothing(*_a, **_k):
        return None

    monkeypatch.setattr(d, "send_control", send_control)
    monkeypatch.setattr(d, "listen_ack", nothing)
    monkeypatch.setattr(d, "listen_close", nothing)
    monkeypatch.setitem(em_state.devices, "dev1", d)
    monkeypatch.setitem(em_state.devices, "dev2", object())
    return d


def _wake_event():
    return {"session": 7, "score": 0.991, "threshold": 0.25, "floor": None,
            "age_ms": 80, "arrived": 0.0, "captured_mono": None,
            "level": None, "peak": None}


@pytest.mark.parametrize("winner", ["dev1", "dev2"])
def test_a_private_barge_flushes_before_it_claims(monkeypatch, winner):
    events = []
    d = _barging_device(events, monkeypatch)

    async def claim(*_a, **_k):
        events.append("claim")
        return winner

    monkeypatch.setattr(em_controller, "_claim_wake", claim)
    monkeypatch.setattr(em_controller.esphome, "can_serve_turn", lambda _id: True)
    monkeypatch.setattr(em_controller.esphome, "abort_ha_run", lambda _id: None)
    monkeypatch.setattr(em_controller, "_wake_heard_at", lambda *_a: 0.0)

    asyncio.run(em_controller._private_barge(d, _wake_event()))

    assert events == ["speaker_flush", "claim"]
    assert d.barge_ceded is (winner != "dev1")


def test_a_barge_while_thinking_sends_no_flush(monkeypatch):
    events = []
    d = _barging_device(events, monkeypatch)
    d.speaking = False
    d.thinking = True

    async def claim(*_a, **_k):
        events.append("claim")
        return "dev1"

    monkeypatch.setattr(em_controller, "_claim_wake", claim)
    monkeypatch.setattr(em_controller.esphome, "can_serve_turn", lambda _id: True)
    monkeypatch.setattr(em_controller.esphome, "cancel_voice_turn", lambda *_a, **_k: None)
    monkeypatch.setattr(em_controller, "_wake_heard_at", lambda *_a: 0.0)

    asyncio.run(em_controller._private_barge(d, _wake_event()))

    assert events == ["claim"]


def test_the_stream_watcher_flushes_before_it_claims():
    # The watcher is a scoring loop over live audio, so its order is pinned
    # in the source: exactly one flush, ahead of the claim.
    src = code_only(def_source("_barge_watcher"))
    assert src.count("speaker_flush") == 1
    assert src.index("speaker_flush") < src.index("_claim_wake(")
